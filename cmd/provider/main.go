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
	"net/http"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/alecthomas/kingpin.v2"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/crossplane/crossplane-runtime/v2/pkg/controller"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	"github.com/crossplane/crossplane-runtime/v2/pkg/ratelimiter"

	"github.com/crossplane/provider-github/apis"
	ghclient "github.com/crossplane/provider-github/internal/clients"
	github "github.com/crossplane/provider-github/internal/controller"
	"github.com/crossplane/provider-github/internal/telemetry"
	"github.com/prometheus/client_golang/prometheus/promhttp"
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

	mgr, err := ctrl.NewManager(ratelimiter.LimitRESTConfig(cfg, *maxReconcileRate), ctrl.Options{
		// SyncPeriod in ctrl.Options has been removed since controller-runtime v0.16.0
		// The recommended way is to move it to cache.Options instead
		Cache: cache.Options{
			SyncPeriod: syncInterval,
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
	}

	if *reconcileTimeout > 0 {
		kingpin.FatalIfError(github.SetupWithTimeout(mgr, o, metrics, *reconcileTimeout), "Cannot setup GitHub controllers")
	} else {
		kingpin.FatalIfError(github.Setup(mgr, o, metrics), "Cannot setup GitHub controllers")
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
