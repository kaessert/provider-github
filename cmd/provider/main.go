/*
Copyright 2020 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/alecthomas/kingpin.v2"
	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/crossplane/crossplane-runtime/v2/pkg/controller"
	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/feature"
	"github.com/crossplane/crossplane-runtime/v2/pkg/gate"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	"github.com/crossplane/crossplane-runtime/v2/pkg/ratelimiter"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/customresourcesgate"

	"github.com/crossplane/provider-github/apis"
	ghclient "github.com/crossplane/provider-github/internal/clients"
	github "github.com/crossplane/provider-github/internal/controller"
	"github.com/crossplane/provider-github/internal/telemetry"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	// safeStartPrecheckTimeout bounds the total time spent retrying the
	// canWatchCRD RBAC precheck. crossplane-rbac-manager has been observed to
	// resolve a fresh provider revision's ClusterRole a couple of seconds after
	// the provider pod starts, so 30s is ample headroom.
	safeStartPrecheckTimeout = 30 * time.Second
	// safeStartPrecheckInterval is the backoff between retries of a denied
	// SelfSubjectAccessReview during the SafeStart precheck.
	safeStartPrecheckInterval = 2 * time.Second
)

func main() {
	var (
		app            = kingpin.New(filepath.Base(os.Args[0]), "GitHub support for Crossplane.").DefaultEnvars()
		debug          = app.Flag("debug", "Run with debug logging.").Short('d').Bool()
		leaderElection = app.Flag("leader-election", "Use leader election for the controller manager.").Short('l').Default("false").OverrideDefaultFromEnvar("LEADER_ELECTION").Bool()

		syncInterval     = app.Flag("sync", "How often all resources will be double-checked for drift from the desired state.").Short('s').Default("1h").Duration()
		pollInterval     = app.Flag("poll", "How often individual resources will be checked for drift from the desired state").Default("1m").Duration()
		maxReconcileRate = app.Flag("max-reconcile-rate", "The global maximum rate per second at which resources may checked for drift from the desired state.").Default("10").Int()
		reconcileTimeout = app.Flag("reconcile-timeout", "Timeout for controller reconciliation operations.").Default("1m").Duration()

		customMetricsAddr = app.Flag("custom-metrics-addr", "The address the custom metric endpoint binds to.").Default(":8081").String()
	)
	kingpin.MustParse(app.Parse(os.Args[1:]))

	zl := zap.New(zap.UseDevMode(*debug))
	log := logging.NewLogrLogger(zl.WithName("provider-github"))
	if *debug {
		// The controller-runtime runs with a no-op logger by default. It is
		// *very* verbose even at info level, so we only provide it a real
		// logger when we're running in debug mode.
		ctrl.SetLogger(zl)
	}

	cfg, err := ctrl.GetConfig()
	kingpin.FatalIfError(err, "Cannot get API server rest config")

	// The CustomResourceDefinition type must be in the scheme before the manager
	// is constructed: the manager resolves every cache.Options.ByObject key
	// while it is being built.
	kingpin.FatalIfError(apiextensionsv1.AddToScheme(clientgoscheme.Scheme), "Cannot add CustomResourceDefinition to scheme")

	mgr, err := ctrl.NewManager(ratelimiter.LimitRESTConfig(cfg, *maxReconcileRate), ctrl.Options{
		// SyncPeriod in ctrl.Options has been removed since controller-runtime v0.16.0
		// The recommended way is to move it to cache.Options instead
		Cache: cache.Options{
			SyncPeriod: syncInterval,
			// The CRD gate reads only group, versions, kind and conditions of a
			// CustomResourceDefinition. Dropping the OpenAPI schemas keeps the
			// cluster-wide CRD informer small.
			ByObject: map[client.Object]cache.ByObject{
				&apiextensionsv1.CustomResourceDefinition{}: {
					Transform: customresourcesgate.TransformStripCRDSchema,
				},
			},
		},

		// Secrets are only ever point-read (credentials, repository and webhook
		// secret values). Reading them straight from the API server avoids a
		// cluster-wide Secret informer.
		Client: client.Options{
			Cache: &client.CacheOptions{DisableFor: []client.Object{&corev1.Secret{}}},
		},

		// controller-runtime uses both ConfigMaps and Leases for leader
		// election by default. Leases expire after 15 seconds, with a
		// 10 second renewal deadline. We've observed leader loss due to
		// renewal deadlines being exceeded when under high load - i.e.
		// hundreds of reconciles per second and ~200rps to the API
		// server. Switching to Leases only and longer leases appears to
		// alleviate this.
		LeaderElection:             *leaderElection,
		LeaderElectionID:           "crossplane-leader-election-provider-github",
		LeaderElectionResourceLock: resourcelock.LeasesResourceLock,
		LeaseDuration:              func() *time.Duration { d := 60 * time.Second; return &d }(),
		RenewDeadline:              func() *time.Duration { d := 50 * time.Second; return &d }(),
	})
	kingpin.FatalIfError(err, "Cannot create controller manager")
	kingpin.FatalIfError(apis.AddToScheme(mgr.GetScheme()), "Cannot add GitHub APIs to scheme")

	// Initialize rate limit metrics and register with controller-runtime manager
	log.Info("Starting rate limit metrics initialization")
	metrics := telemetry.NewRateLimitMetrics(mgr)
	log.Info("Rate limit metrics initialized and registered with controller-runtime")

	o := controller.Options{
		Logger:                  log,
		MaxConcurrentReconciles: *maxReconcileRate,
		PollInterval:            *pollInterval,
		GlobalRateLimiter:       ratelimiter.NewGlobal(*maxReconcileRate),
		Features:                &feature.Flags{},
	}

	precheckCtx, cancel := context.WithTimeout(context.Background(), safeStartPrecheckTimeout)
	defer cancel()
	canSafeStart, err := canWatchCRD(precheckCtx, mgr)
	kingpin.FatalIfError(err, "SafeStart precheck failed")
	if canSafeStart {
		crdGate := new(gate.Gate[schema.GroupVersionKind])
		o.Gate = crdGate
		gateOpts := controller.Options{
			Gate:                    crdGate,
			Logger:                  log,
			MaxConcurrentReconciles: 1,
		}
		kingpin.FatalIfError(customresourcesgate.Setup(mgr, gateOpts), "Cannot setup CRD gate")
		kingpin.FatalIfError(github.SetupGated(mgr, o, metrics, *reconcileTimeout), "Cannot setup GitHub controllers")
	} else {
		log.Info("Provider has missing RBAC permissions for watching CRDs, controller SafeStart capability will be disabled")
		kingpin.FatalIfError(github.Setup(mgr, o, metrics, *reconcileTimeout), "Cannot setup GitHub controllers")
	}

	// Start custom metrics server on a different port
	go func() {
		log.Info("Starting custom metrics server", "addr", *customMetricsAddr)
		http.Handle("/metrics", promhttp.Handler())
		if err := http.ListenAndServe(*customMetricsAddr, nil); err != nil {
			zl.Error(err, "Custom metrics server failed to start")
		}
	}()

	// Start periodic cleanup of expired GitHub clients
	go func() {
		ticker := time.NewTicker(15 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			ghclient.CleanupExpiredServices()
			log.Debug("Cleaned up expired GitHub clients")
		}
	}()

	kingpin.FatalIfError(mgr.Start(ctrl.SetupSignalHandler()), "Cannot start controller manager")
}

// canWatchCRD reports whether the provider's service account may get, list and
// watch CustomResourceDefinitions, which the CRD gate needs. It polls until
// the permission is granted or ctx expires: crossplane-rbac-manager creates a
// provider revision's ClusterRole asynchronously, after the ServiceAccount and
// ClusterRoleBinding exist, so a single early denial means "not yet", not
// "never". A denial that outlasts ctx is not an error; the caller falls back to
// starting every controller eagerly.
func canWatchCRD(ctx context.Context, mgr ctrl.Manager) (bool, error) {
	if err := authv1.AddToScheme(mgr.GetScheme()); err != nil {
		return false, err
	}

	var allowed bool
	pollErr := wait.PollUntilContextCancel(ctx, safeStartPrecheckInterval, true, func(pollCtx context.Context) (bool, error) {
		ok, err := hasCRDWatchPermissions(pollCtx, mgr)
		if err != nil {
			return false, err
		}
		allowed = ok
		// A denial keeps polling until the deadline; permission stops the poll.
		return ok, nil
	})
	if pollErr != nil && !wait.Interrupted(pollErr) {
		return false, pollErr
	}
	// wait.Interrupted means the deadline was reached without every verb being
	// allowed: a denial, not an error.
	return allowed, nil
}

// hasCRDWatchPermissions runs one round of SelfSubjectAccessReview for get,
// list and watch on CustomResourceDefinitions and reports whether every verb
// is allowed.
func hasCRDWatchPermissions(ctx context.Context, mgr ctrl.Manager) (bool, error) {
	for _, verb := range []string{"get", "list", "watch"} {
		sar := &authv1.SelfSubjectAccessReview{
			Spec: authv1.SelfSubjectAccessReviewSpec{
				ResourceAttributes: &authv1.ResourceAttributes{
					Group:    "apiextensions.k8s.io",
					Resource: "customresourcedefinitions",
					Verb:     verb,
				},
			},
		}
		if err := mgr.GetClient().Create(ctx, sar); err != nil {
			return false, errors.Wrapf(err, "unable to perform RBAC check for verb %s on CustomResourceDefinitions", verb)
		}
		if !sar.Status.Allowed {
			return false, nil
		}
	}
	return true, nil
}
