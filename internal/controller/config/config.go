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

package config

import (
	"github.com/crossplane/crossplane-runtime/v2/pkg/controller"
	"github.com/crossplane/crossplane-runtime/v2/pkg/event"
	"github.com/crossplane/crossplane-runtime/v2/pkg/ratelimiter"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/providerconfig"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	ctrl "sigs.k8s.io/controller-runtime"

	clusterv1alpha1 "github.com/crossplane/provider-github/apis/cluster/v1alpha1"
	namespacedv1alpha1 "github.com/crossplane/provider-github/apis/namespaced/v1alpha1"
	"github.com/crossplane/provider-github/internal/telemetry"
)

// Setup adds controllers that reconcile ProviderConfigs by accounting for
// their current usage. Three ProviderConfig Kinds exist across the two API
// groups this provider serves:
//   - github.crossplane.io ProviderConfig (cluster-scoped, legacy)
//   - github.m.crossplane.io ProviderConfig (namespaced)
//   - github.m.crossplane.io ClusterProviderConfig (cluster-scoped)
//
// The two github.m.crossplane.io Kinds share one ProviderConfigUsage Kind: the
// usage record carries the Kind of the config it tracks.
func Setup(mgr ctrl.Manager, o controller.Options, _ *telemetry.RateLimitMetrics) error {
	if err := setupLegacy(mgr, o); err != nil {
		return err
	}
	if err := setupNamespaced(mgr, o); err != nil {
		return err
	}
	return setupCluster(mgr, o)
}

// setupLegacy reconciles the cluster-scoped ProviderConfig in the
// github.crossplane.io group.
func setupLegacy(mgr ctrl.Manager, o controller.Options) error {
	name := providerconfig.ControllerName(clusterv1alpha1.ProviderConfigGroupKind)

	of := resource.ProviderConfigKinds{
		Config:    clusterv1alpha1.ProviderConfigGroupVersionKind,
		Usage:     clusterv1alpha1.ProviderConfigUsageGroupVersionKind,
		UsageList: clusterv1alpha1.ProviderConfigUsageListGroupVersionKind,
	}

	r := providerconfig.NewReconciler(mgr, of,
		providerconfig.WithLogger(o.Logger.WithValues("controller", name)),
		providerconfig.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name)))) //nolint:staticcheck // crossplane-runtime's event.NewAPIRecorder still takes the record.EventRecorder that GetEventRecorderFor returns; the replacement events API is not accepted by it yet.

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		WithOptions(o.ForControllerRuntime()).
		For(&clusterv1alpha1.ProviderConfig{}).
		Watches(&clusterv1alpha1.ProviderConfigUsage{}, &resource.EnqueueRequestForProviderConfig{}).
		Complete(ratelimiter.NewReconciler(name, r, o.GlobalRateLimiter))
}

// setupNamespaced reconciles the namespaced ProviderConfig in the
// github.m.crossplane.io group.
func setupNamespaced(mgr ctrl.Manager, o controller.Options) error {
	name := providerconfig.ControllerName(namespacedv1alpha1.ProviderConfigGroupKind)

	of := resource.ProviderConfigKinds{
		Config:    namespacedv1alpha1.ProviderConfigGroupVersionKind,
		Usage:     namespacedv1alpha1.ProviderConfigUsageGroupVersionKind,
		UsageList: namespacedv1alpha1.ProviderConfigUsageListGroupVersionKind,
	}

	r := providerconfig.NewReconciler(mgr, of,
		providerconfig.WithLogger(o.Logger.WithValues("controller", name)),
		providerconfig.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name)))) //nolint:staticcheck // crossplane-runtime's event.NewAPIRecorder still takes the record.EventRecorder that GetEventRecorderFor returns; the replacement events API is not accepted by it yet.

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		WithOptions(o.ForControllerRuntime()).
		For(&namespacedv1alpha1.ProviderConfig{}).
		Watches(&namespacedv1alpha1.ProviderConfigUsage{}, &resource.EnqueueRequestForProviderConfig{Kind: namespacedv1alpha1.ProviderConfigKind}).
		Complete(ratelimiter.NewReconciler(name, r, o.GlobalRateLimiter))
}

// setupCluster reconciles the cluster-scoped ClusterProviderConfig in the
// github.m.crossplane.io group. It shares the ProviderConfigUsage Kind with the
// namespaced ProviderConfig; the usage record's providerConfigRef Kind tells
// the two apart.
func setupCluster(mgr ctrl.Manager, o controller.Options) error {
	name := providerconfig.ControllerName(namespacedv1alpha1.ClusterProviderConfigGroupKind)

	of := resource.ProviderConfigKinds{
		Config:    namespacedv1alpha1.ClusterProviderConfigGroupVersionKind,
		Usage:     namespacedv1alpha1.ProviderConfigUsageGroupVersionKind,
		UsageList: namespacedv1alpha1.ProviderConfigUsageListGroupVersionKind,
	}

	r := providerconfig.NewReconciler(mgr, of,
		providerconfig.WithLogger(o.Logger.WithValues("controller", name)),
		providerconfig.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name)))) //nolint:staticcheck // crossplane-runtime's event.NewAPIRecorder still takes the record.EventRecorder that GetEventRecorderFor returns; the replacement events API is not accepted by it yet.

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		WithOptions(o.ForControllerRuntime()).
		For(&namespacedv1alpha1.ClusterProviderConfig{}).
		Watches(&namespacedv1alpha1.ProviderConfigUsage{}, &resource.EnqueueRequestForProviderConfig{Kind: namespacedv1alpha1.ClusterProviderConfigKind}).
		Complete(ratelimiter.NewReconciler(name, r, o.GlobalRateLimiter))
}
