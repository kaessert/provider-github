/*
Copyright 2026 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package clients

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"

	apisv1alpha1 "github.com/crossplane/provider-github/apis/cluster/v1alpha1"
)

// resolveAllCredentials extracts every credential entry on pc (the primary
// Credentials field plus any AdditionalCredentials) into a slice of raw
// credential strings, in declaration order.
//
// Returns an error pointing at the exact offending entry when extraction
// fails or when the extractor returns empty bytes (typically a missing
// Secret key or an unpopulated value, neither of which the extractor
// surfaces as an error on its own).
func resolveAllCredentials(ctx context.Context, kube client.Client, pc *apisv1alpha1.ProviderConfig) ([]string, error) {
	return resolveCredentials(ctx, kube, pc.Spec)
}

// resolveCredentials is resolveAllCredentials for a bare ProviderConfigSpec.
func resolveCredentials(ctx context.Context, kube client.Client, spec apisv1alpha1.ProviderConfigSpec) ([]string, error) {
	all := make([]apisv1alpha1.ProviderCredentials, 0, 1+len(spec.AdditionalCredentials))
	all = append(all, spec.Credentials)
	all = append(all, spec.AdditionalCredentials...)

	resolved := make([]string, 0, len(all))
	for i, cd := range all {
		data, err := resource.CommonCredentialExtractor(ctx, cd.Source, kube, cd.CommonCredentialSelectors)
		if err != nil {
			return nil, errors.Wrapf(err, "cannot get credentials at index %d", i)
		}
		if len(data) == 0 {
			return nil, errors.New(describeEmptyCredentials(i, cd))
		}
		resolved = append(resolved, string(data))
	}
	return resolved, nil
}

// describeEmptyCredentials builds an operator-friendly error message for a
// credential entry whose extraction returned empty bytes. The most useful
// thing to point at is the underlying reference (Secret namespace/name/key,
// env var name, filesystem path) — that's what the operator needs to fix.
func describeEmptyCredentials(index int, cd apisv1alpha1.ProviderCredentials) string {
	switch {
	case cd.SecretRef != nil:
		return fmt.Sprintf("credentials at index %d: secret %s/%s key %q is empty or missing", index, cd.SecretRef.Namespace, cd.SecretRef.Name, cd.SecretRef.Key)
	case cd.Env != nil:
		return fmt.Sprintf("credentials at index %d: env var %q is empty or unset", index, cd.Env.Name)
	case cd.Fs != nil:
		return fmt.Sprintf("credentials at index %d: file %q is empty or missing", index, cd.Fs.Path)
	default:
		return fmt.Sprintf("credentials at index %d: extracted empty bytes (source=%s)", index, cd.Source)
	}
}
