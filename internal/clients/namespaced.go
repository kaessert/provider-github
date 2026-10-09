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

package clients

import (
	"context"

	"github.com/pkg/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	apisv1alpha1 "github.com/crossplane/provider-github/apis/cluster/v1alpha1"
	namespacedv1alpha1 "github.com/crossplane/provider-github/apis/namespaced/v1alpha1"
	"github.com/crossplane/provider-github/internal/telemetry"
)

const (
	errNoProviderConfigRef  = "providerConfigRef is not set"
	errGetNamespacedPC      = "cannot get ProviderConfig"
	errGetClusterPC         = "cannot get ClusterProviderConfig"
	errUnsupportedPCKind    = "unsupported provider config kind"
	providerConfigKind      = "ProviderConfig"
	clusterProviderConfKind = "ClusterProviderConfig"
)

// ResolveAndConnectNamespaced is ResolveAndConnect for a namespaced managed
// resource. ref is the resource's providerConfigRef, which names either a
// ProviderConfig in namespace or a ClusterProviderConfig.
func ResolveAndConnectNamespaced(ctx context.Context, kube client.Client, ref *xpv2.ProviderConfigReference, namespace string, metrics *telemetry.RateLimitMetrics, org string) (*Client, error) {
	spec, err := namespacedConfigSpec(ctx, kube, ref, namespace)
	if err != nil {
		return nil, err
	}
	return resolveAndConnect(ctx, kube, spec, metrics, org)
}

// namespacedConfigSpec fetches the config ref points at and returns its spec in
// the form the credential resolver takes.
func namespacedConfigSpec(ctx context.Context, kube client.Client, ref *xpv2.ProviderConfigReference, namespace string) (apisv1alpha1.ProviderConfigSpec, error) {
	if ref == nil {
		return apisv1alpha1.ProviderConfigSpec{}, errors.New(errNoProviderConfigRef)
	}

	switch ref.Kind {
	case providerConfigKind:
		pc := &namespacedv1alpha1.ProviderConfig{}
		if err := kube.Get(ctx, client.ObjectKey{Name: ref.Name, Namespace: namespace}, pc); err != nil {
			return apisv1alpha1.ProviderConfigSpec{}, errors.Wrap(err, errGetNamespacedPC)
		}
		return clusterSpec(pc.Spec, namespace), nil
	case clusterProviderConfKind:
		cpc := &namespacedv1alpha1.ClusterProviderConfig{}
		if err := kube.Get(ctx, client.ObjectKey{Name: ref.Name}, cpc); err != nil {
			return apisv1alpha1.ProviderConfigSpec{}, errors.Wrap(err, errGetClusterPC)
		}
		return clusterSpec(cpc.Spec, ""), nil
	default:
		return apisv1alpha1.ProviderConfigSpec{}, errors.Errorf("%s: %s", errUnsupportedPCKind, ref.Kind)
	}
}

// clusterSpec converts a namespaced config spec to the cluster-scoped form. A
// non-empty namespace pins every credential Secret to it, discarding the
// namespace the spec carried: a namespaced ProviderConfig is writable by the
// namespace's tenants, and the provider reads Secrets cluster-wide, so an
// unpinned namespace would let a tenant name a Secret in any namespace.
func clusterSpec(in namespacedv1alpha1.ProviderConfigSpec, namespace string) apisv1alpha1.ProviderConfigSpec {
	out := apisv1alpha1.ProviderConfigSpec{
		Credentials: clusterCredentials(in.Credentials, namespace),
	}
	for _, c := range in.AdditionalCredentials {
		out.AdditionalCredentials = append(out.AdditionalCredentials, clusterCredentials(c, namespace))
	}
	return out
}

func clusterCredentials(in namespacedv1alpha1.ProviderCredentials, namespace string) apisv1alpha1.ProviderCredentials {
	out := apisv1alpha1.ProviderCredentials{
		Source:                    in.Source,
		CommonCredentialSelectors: in.CommonCredentialSelectors,
	}
	if namespace != "" && in.SecretRef != nil {
		pinned := *in.SecretRef
		out.SecretRef = &pinned
		out.SecretRef.Namespace = namespace
	}
	return out
}
