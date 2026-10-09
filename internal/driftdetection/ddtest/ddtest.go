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

// Package ddtest holds the test helper every controller package uses to prove
// that wrapping its external client with the drift detection decorator changes
// nothing for a resource that carries no spec.driftDetection block. It is
// imported by tests only.
package ddtest

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/crossplane/crossplane-runtime/v2/pkg/fieldpath"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"

	"github.com/crossplane/provider-github/internal/driftdetection"
)

// RequirePassThrough observes a fresh resource through a bare client and
// through the same kind of client wrapped by the decorator, and fails the test
// unless the two are indistinguishable: same observation, same error, same
// resulting resource (spec, status and conditions, ignoring timestamps), and no
// DriftDetected condition.
//
// newClient must return a new, independent client on every call, and newCR a
// new resource with no spec.driftDetection block.
func RequirePassThrough(t *testing.T, newClient func() managed.ExternalClient, newCR func() resource.Managed) {
	t.Helper()
	ctx := context.Background()

	bareCR := newCR()
	cfg, err := driftdetection.ReadConfig(bareCR)
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}
	if cfg.Active() {
		t.Fatalf("fixture must carry no driftDetection block, got an active config %+v", cfg)
	}

	bareObs, bareErr := newClient().Observe(ctx, bareCR)

	wrappedCR := newCR()
	wrappedObs, wrappedErr := driftdetection.WrapClient[resource.Managed](newClient()).Observe(ctx, wrappedCR)

	if (bareErr == nil) != (wrappedErr == nil) || (bareErr != nil && bareErr.Error() != wrappedErr.Error()) {
		t.Fatalf("Observe error changed by the wrapper: bare=%v wrapped=%v", bareErr, wrappedErr)
	}
	if diff := cmp.Diff(bareObs, wrappedObs); diff != "" {
		t.Errorf("Observe result changed by the wrapper (-bare +wrapped):\n%s", diff)
	}
	if diff := cmp.Diff(bareCR, wrappedCR, cmpopts.IgnoreTypes(metav1.Time{})); diff != "" {
		t.Errorf("resource changed by the wrapper (-bare +wrapped):\n%s", diff)
	}
	p, err := fieldpath.PaveObject(wrappedCR)
	if err != nil {
		t.Fatalf("PaveObject: %v", err)
	}
	conds, _ := p.GetValue("status.conditions")
	list, _ := conds.([]any)
	for _, c := range list {
		if m, ok := c.(map[string]any); ok && m["type"] == string(driftdetection.TypeDriftDetected) {
			t.Errorf("wrapper set a %s condition on a resource with no driftDetection block: %v", driftdetection.TypeDriftDetected, m)
		}
	}
}

// Convert returns from re-expressed as a To, through its JSON form. It is how a
// test builds the namespaced twin of a cluster-scoped fixture: both scopes
// share their field names.
func Convert[To resource.Managed](t *testing.T, from resource.Managed, to To) To {
	t.Helper()
	data, err := json.Marshal(from)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if err := json.Unmarshal(data, to); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	return to
}
