/*
Copyright 2026 The Crossplane Authors.

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

package team

import (
	"context"
	"time"

	"github.com/crossplane/crossplane-runtime/v2/pkg/controller"
	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/event"
	"github.com/crossplane/crossplane-runtime/v2/pkg/ratelimiter"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1alpha1 "github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	namespacedv1alpha1 "github.com/crossplane/provider-github/apis/namespaced/organizations/v1alpha1"
	namespacedapis "github.com/crossplane/provider-github/apis/namespaced/v1alpha1"
	ghclient "github.com/crossplane/provider-github/internal/clients"
	"github.com/crossplane/provider-github/internal/controller/scopebridge"
	"github.com/crossplane/provider-github/internal/driftdetection"
	"github.com/crossplane/provider-github/internal/telemetry"
)

// SetupNamespaced adds a controller that reconciles namespaced Team managed resources.
func SetupNamespaced(mgr ctrl.Manager, o controller.Options, metrics *telemetry.RateLimitMetrics) error {
	return SetupNamespacedWithTimeout(mgr, o, metrics, 0)
}

// SetupNamespacedWithTimeout adds a controller that reconciles namespaced Team managed resources with configurable timeout.
func SetupNamespacedWithTimeout(mgr ctrl.Manager, o controller.Options, metrics *telemetry.RateLimitMetrics, timeout time.Duration) error {
	name := managed.ControllerName(namespacedv1alpha1.TeamGroupKind)

	reconcilerOptions := []managed.ReconcilerOption{
		managed.WithExternalConnector(driftdetection.WrapConnector[resource.Managed](&namespacedConnector{
			kube:    mgr.GetClient(),
			usage:   resource.NewProviderConfigUsageTracker(mgr.GetClient(), &namespacedapis.ProviderConfigUsage{}),
			metrics: metrics})),
		managed.WithLogger(o.Logger.WithValues("controller", name)),
		managed.WithPollInterval(o.PollInterval),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))), //nolint:staticcheck // crossplane-runtime's event.NewAPIRecorder still takes the record.EventRecorder that GetEventRecorderFor returns; the replacement events API is not accepted by it yet.
		managed.WithManagementPolicies(),
	}

	if timeout > 0 {
		reconcilerOptions = append(reconcilerOptions, managed.WithTimeout(timeout))
	}

	r := managed.NewReconciler(mgr,
		resource.ManagedKind(namespacedv1alpha1.TeamGroupVersionKind),
		reconcilerOptions...)

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		WithOptions(o.ForControllerRuntime()).
		WithEventFilter(resource.DesiredStateChanged()).
		For(&namespacedv1alpha1.Team{}).
		Complete(ratelimiter.NewReconciler(name, r, o.GlobalRateLimiter))
}

type namespacedConnector struct {
	kube    client.Client
	usage   *resource.ProviderConfigUsageTracker
	metrics *telemetry.RateLimitMetrics
}

// Connect builds the cluster-scoped external client for Team and wraps it so
// it serves the namespaced variant: one implementation backs both scopes.
func (c *namespacedConnector) Connect(ctx context.Context, mg resource.Managed) (managed.ExternalClient, error) {
	cr, ok := mg.(*namespacedv1alpha1.Team)
	if !ok {
		return nil, errors.New(errNotTeam)
	}

	if err := c.usage.Track(ctx, cr); err != nil {
		return nil, errors.Wrap(err, errTrackPCUsage)
	}

	gh, err := ghclient.ResolveAndConnectNamespaced(ctx, c.kube, cr.GetProviderConfigReference(), cr.GetNamespace(), c.metrics, cr.Spec.ForProvider.Org)
	if err != nil {
		return nil, errors.Wrap(err, errNewClient)
	}

	return scopebridge.New[*namespacedv1alpha1.Team](
		&external{github: gh},
		func() *clusterv1alpha1.Team { return &clusterv1alpha1.Team{} },
	), nil
}
