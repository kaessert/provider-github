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

package driftdetection

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"

	clusterv1alpha1 "github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	dd "github.com/crossplane/provider-github/apis/common/driftdetection"
)

// Organization is the one kind whose status.atProvider carries a counterpart
// for a forProvider field (description), so it is the type the substitution
// tests run against. The fake controller below fills that counterpart in, which
// the real Organization controller does not do today; tests therefore install
// the allowlist entry themselves with allowDescription.

const (
	ignoreDescription = "forProvider.description"
	orgKey            = "Organization:" + ignoreDescription
)

// remote stands in for the GitHub organization a real controller talks to.
//
// Its update is a whole-object write built from spec, the shape that can lose
// data: a payload built from spec reverts whatever the external owner set.
type remote struct {
	description string
	repos       []string
	observes    int
	updates     int
}

func (r *remote) client() managed.TypedExternalClient[*clusterv1alpha1.Organization] {
	return managed.TypedExternalClientFns[*clusterv1alpha1.Organization]{
		ObserveFn: func(_ context.Context, mg *clusterv1alpha1.Organization) (managed.ExternalObservation, error) {
			r.observes++
			mg.Status.AtProvider.Description = r.description
			return managed.ExternalObservation{
				ResourceExists:   true,
				ResourceUpToDate: r.description == mg.Spec.ForProvider.Description && r.reposMatch(mg),
			}, nil
		},
		UpdateFn: func(_ context.Context, mg *clusterv1alpha1.Organization) (managed.ExternalUpdate, error) {
			r.updates++
			r.description = mg.Spec.ForProvider.Description
			r.repos = repoNames(mg)
			return managed.ExternalUpdate{}, nil
		},
		CreateFn: func(_ context.Context, mg *clusterv1alpha1.Organization) (managed.ExternalCreation, error) {
			r.description = mg.Spec.ForProvider.Description
			r.repos = repoNames(mg)
			return managed.ExternalCreation{}, nil
		},
		DeleteFn: func(_ context.Context, _ *clusterv1alpha1.Organization) (managed.ExternalDelete, error) {
			return managed.ExternalDelete{}, nil
		},
		DisconnectFn: func(_ context.Context) error { return nil },
	}
}

func (r *remote) reposMatch(mg *clusterv1alpha1.Organization) bool {
	return slices.Equal(r.repos, repoNames(mg))
}

func repoNames(mg *clusterv1alpha1.Organization) []string {
	out := make([]string, 0, len(mg.Spec.ForProvider.Actions.EnabledRepos))
	for _, r := range mg.Spec.ForProvider.Actions.EnabledRepos {
		out = append(out, r.Repo)
	}
	return out
}

// allowDescription marks forProvider.description observable for the duration of
// one test, standing in for a controller that mirrors it.
func allowDescription(t *testing.T) {
	t.Helper()
	eligibleIgnorePaths[orgKey] = true
	t.Cleanup(func() { delete(eligibleIgnorePaths, orgKey) })
}

func mr(cfg *dd.DriftDetection, description string, repos ...string) *clusterv1alpha1.Organization {
	cr := &clusterv1alpha1.Organization{}
	cr.Spec.DriftDetection = cfg
	cr.Spec.ForProvider.Description = description
	for _, r := range repos {
		cr.Spec.ForProvider.Actions.EnabledRepos = append(cr.Spec.ForProvider.Actions.EnabledRepos,
			clusterv1alpha1.ActionEnabledRepo{Repo: r})
	}
	return cr
}

func config(mode dd.Mode, paths ...string) *dd.DriftDetection {
	c := &dd.DriftDetection{Mode: mode}
	if len(paths) > 0 {
		c.Ignore = []dd.IgnoreRule{{Paths: paths}}
	}
	return c
}

func driftReason(cr *clusterv1alpha1.Organization) (corev1.ConditionStatus, string, bool) {
	for _, c := range cr.Status.Conditions {
		if c.Type == TypeDriftDetected {
			return c.Status, string(c.Reason), true
		}
	}
	return "", "", false
}

// ─── behaviour ───────────────────────────────────────────────────────────────

// With no driftDetection block the wrapper is transparent. Adding the field to
// an API must not change how existing resources reconcile.
func TestNoConfigIsTransparent(t *testing.T) {
	r := &remote{description: "set-by-someone-else"}
	cr := mr(nil, "mine")
	before := cr.DeepCopy()

	obs, err := WrapClient(r.client()).Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if obs.ResourceUpToDate {
		t.Error("want drift reported when nothing is ignored")
	}
	if r.observes != 1 {
		t.Errorf("want 1 inner Observe, got %d", r.observes)
	}
	if _, _, ok := driftReason(cr); ok {
		t.Error("want no DriftDetected condition when driftDetection is absent")
	}
	if diff := cmp.Diff(before.Spec, cr.Spec); diff != "" {
		t.Errorf("spec changed (-want +got):\n%s", diff)
	}
}

// An enabled block with no ignore paths is the same as no block.
func TestEnabledWithoutIgnoreIsTransparent(t *testing.T) {
	r := &remote{description: "set-by-someone-else"}
	cr := mr(config(dd.ModeEnabled), "mine")

	e := WrapClient(r.client())
	ctx := context.Background()
	obs, err := e.Observe(ctx, cr)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if obs.ResourceUpToDate || r.observes != 1 {
		t.Errorf("want drift reported from one read, got upToDate=%v observes=%d", obs.ResourceUpToDate, r.observes)
	}
	if _, err := e.Update(ctx, cr); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if r.description != "mine" {
		t.Errorf("want the spec value written, got %q", r.description)
	}
	if _, _, ok := driftReason(cr); ok {
		t.Error("want no DriftDetected condition")
	}
}

func TestIgnoredPathSuppressesDrift(t *testing.T) {
	allowDescription(t)
	r := &remote{description: "owned-externally"}
	cr := mr(config(dd.ModeEnabled, ignoreDescription), "seed")

	obs, err := WrapClient(r.client()).Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !obs.ResourceUpToDate {
		t.Error("want up to date: the only difference is on an ignored path")
	}
	if cr.Spec.ForProvider.Description != "seed" {
		t.Errorf("user spec was mutated: got %q", cr.Spec.ForProvider.Description)
	}
	if got := cr.Status.AtProvider.Description; got != "owned-externally" {
		t.Errorf("atProvider must report the truth, got %q", got)
	}
	if st, reason, ok := driftReason(cr); !ok || st != corev1.ConditionTrue || reason != string(ReasonIgnored) {
		t.Errorf("want DriftDetected=True/DriftIgnored, got %v/%v (present=%v)", st, reason, ok)
	}
}

func TestIgnoredPathDoesNotMaskOtherDrift(t *testing.T) {
	allowDescription(t)
	r := &remote{description: "owned-externally", repos: []string{"a"}}
	cr := mr(config(dd.ModeEnabled, ignoreDescription), "seed", "a", "b")

	obs, err := WrapClient(r.client()).Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if obs.ResourceUpToDate {
		t.Error("want drift reported: the repository list differs and is not ignored")
	}
	if _, reason, _ := driftReason(cr); reason != string(ReasonDrifted) {
		t.Errorf("want reason Drifted, got %v", reason)
	}
}

// The write path: correcting drift must not revert an ignored field.
func TestUpdateCarriesObservedValueForIgnoredPath(t *testing.T) {
	allowDescription(t)
	r := &remote{description: "owned-externally", repos: []string{"a"}}
	cr := mr(config(dd.ModeEnabled, ignoreDescription), "seed", "a", "b") // repos: genuine drift

	e := WrapClient(r.client())
	ctx := context.Background()
	if _, err := e.Observe(ctx, cr); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if _, err := e.Update(ctx, cr); err != nil {
		t.Fatalf("Update: %v", err)
	}

	if !slices.Equal(r.repos, []string{"a", "b"}) {
		t.Errorf("owned field not reconciled: want [a b], got %v", r.repos)
	}
	if r.description != "owned-externally" {
		t.Errorf("ignored field reverted by the update: want owned-externally, got %q "+
			"(the failure a compare-only ignore produces under a whole-object write)", r.description)
	}
	if cr.Spec.ForProvider.Description != "seed" {
		t.Errorf("user spec was mutated: got %q", cr.Spec.ForProvider.Description)
	}
}

func TestCreateSendsSeedValue(t *testing.T) {
	allowDescription(t)
	r := &remote{}
	cr := mr(config(dd.ModeEnabled, ignoreDescription), "seed")

	if _, err := WrapClient(r.client()).Create(context.Background(), cr); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if r.description != "seed" {
		t.Errorf("want seed sent on create, got %q", r.description)
	}
}

func TestDeletePassesThrough(t *testing.T) {
	deleted := false
	inner := managed.TypedExternalClientFns[*clusterv1alpha1.Organization]{
		DeleteFn: func(_ context.Context, _ *clusterv1alpha1.Organization) (managed.ExternalDelete, error) {
			deleted = true
			return managed.ExternalDelete{}, nil
		},
	}
	if _, err := WrapClient[*clusterv1alpha1.Organization](inner).Delete(context.Background(), mr(nil, "x")); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !deleted {
		t.Error("want Delete delegated to the wrapped client")
	}
}

func TestWarnReportsWithoutCorrecting(t *testing.T) {
	r := &remote{description: "upstream"}
	cr := mr(config(dd.ModeWarn), "mine")

	obs, err := WrapClient(r.client()).Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !obs.ResourceUpToDate {
		t.Error("warn must not trigger an update")
	}
	if st, reason, ok := driftReason(cr); !ok || st != corev1.ConditionTrue || reason != string(ReasonDrifted) {
		t.Errorf("want DriftDetected=True/Drifted, got %v/%v (present=%v)", st, reason, ok)
	}
	if r.observes != 1 {
		t.Errorf("warn must not cost a second read, got %d", r.observes)
	}
}

func TestWarnInSyncReportsInSync(t *testing.T) {
	r := &remote{description: "mine"}
	cr := mr(config(dd.ModeWarn), "mine")

	if _, err := WrapClient(r.client()).Observe(context.Background(), cr); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if st, reason, ok := driftReason(cr); !ok || st != corev1.ConditionFalse || reason != string(ReasonInSync) {
		t.Errorf("want DriftDetected=False/InSync, got %v/%v (present=%v)", st, reason, ok)
	}
}

func TestDisabledSuppressesEverything(t *testing.T) {
	r := &remote{description: "upstream"}
	cr := mr(config(dd.ModeDisabled), "mine")

	obs, err := WrapClient(r.client()).Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !obs.ResourceUpToDate {
		t.Error("disabled must never report drift")
	}
	if _, _, ok := driftReason(cr); ok {
		t.Error("disabled must not set a drift condition")
	}
}

func TestNoSecondReadWhenInSync(t *testing.T) {
	allowDescription(t)
	r := &remote{description: "same"}
	cr := mr(config(dd.ModeEnabled, ignoreDescription), "same")

	if _, err := WrapClient(r.client()).Observe(context.Background(), cr); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if r.observes != 1 {
		t.Errorf("want 1 inner Observe when in sync, got %d", r.observes)
	}
}

func TestAbsentResourceIsReturnedUntouched(t *testing.T) {
	allowDescription(t)
	inner := managed.TypedExternalClientFns[*clusterv1alpha1.Organization]{
		ObserveFn: func(_ context.Context, _ *clusterv1alpha1.Organization) (managed.ExternalObservation, error) {
			return managed.ExternalObservation{ResourceExists: false}, nil
		},
	}
	cr := mr(config(dd.ModeEnabled, ignoreDescription), "seed")
	obs, err := WrapClient[*clusterv1alpha1.Organization](inner).Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if obs.ResourceExists {
		t.Error("want a missing resource reported as missing")
	}
	if _, _, ok := driftReason(cr); ok {
		t.Error("want no drift condition for a resource that does not exist")
	}
}

func TestInnerObserveErrorPropagates(t *testing.T) {
	boom := errors.New("boom")
	inner := managed.TypedExternalClientFns[*clusterv1alpha1.Organization]{
		ObserveFn: func(_ context.Context, _ *clusterv1alpha1.Organization) (managed.ExternalObservation, error) {
			return managed.ExternalObservation{}, boom
		},
	}
	if _, err := WrapClient[*clusterv1alpha1.Organization](inner).Observe(context.Background(), mr(nil, "x")); !errors.Is(err, boom) {
		t.Errorf("want the wrapped client's error, got %v", err)
	}
}

// ─── freshness and race safety ───────────────────────────────────────────────

// Update must consume the observation captured during Observe, never re-read
// status.atProvider. Re-reading would pick up whatever the object carries at
// that moment, which for any path that does not repopulate it is a previous
// reconcile's value.
func TestUpdateUsesSnapshotNotStatusReread(t *testing.T) {
	allowDescription(t)
	r := &remote{description: "observed-now", repos: []string{"a"}}
	cr := mr(config(dd.ModeEnabled, ignoreDescription), "seed", "a", "b")

	e := WrapClient(r.client())
	ctx := context.Background()
	if _, err := e.Observe(ctx, cr); err != nil {
		t.Fatalf("Observe: %v", err)
	}

	// Simulate anything that rewrites status between Observe and Update:
	// a stale cache decode, a status patch, a second controller.
	cr.Status.AtProvider.Description = "STALE-OR-TAMPERED"

	if _, err := e.Update(ctx, cr); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if r.description != "observed-now" {
		t.Errorf("Update read status.atProvider instead of the captured snapshot: got %q", r.description)
	}
}

// Update without a preceding Observe must fail closed. Falling back to the spec
// value would revert the external owner outright.
func TestUpdateFailsClosedWithoutObserve(t *testing.T) {
	allowDescription(t)
	r := &remote{description: "owned-externally"}
	cr := mr(config(dd.ModeEnabled, ignoreDescription), "seed")

	if _, err := WrapClient(r.client()).Update(context.Background(), cr); err == nil {
		t.Error("want Update to fail closed when no observation was captured")
	}
	if r.updates != 0 {
		t.Errorf("want no write issued, got %d", r.updates)
	}
	if r.description != "owned-externally" {
		t.Errorf("external owner's value was overwritten: got %q", r.description)
	}
}

// Disconnect ends the per-reconcile lifetime; a client reused afterwards must
// not apply a stale snapshot.
func TestDisconnectClearsSnapshot(t *testing.T) {
	allowDescription(t)
	r := &remote{description: "observed"}
	cr := mr(config(dd.ModeEnabled, ignoreDescription), "seed")

	e := WrapClient(r.client())
	ctx := context.Background()
	if _, err := e.Observe(ctx, cr); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if err := e.Disconnect(ctx); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if _, err := e.Update(ctx, cr); err == nil {
		t.Error("want Update to fail closed after Disconnect dropped the snapshot")
	}
}

// The reconciler's managed resource must never be mutated by substitution: it
// may be shared with a cache, and a mutated spec would be persisted by the
// ResourceLateInitialized path.
func TestInputResourceIsNeverMutated(t *testing.T) {
	allowDescription(t)
	r := &remote{description: "upstream", repos: []string{"a"}}
	cr := mr(config(dd.ModeEnabled, ignoreDescription), "seed", "a", "b")
	before := cr.Spec.DeepCopy()

	e := WrapClient(r.client())
	ctx := context.Background()
	if _, err := e.Observe(ctx, cr); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if _, err := e.Update(ctx, cr); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if diff := cmp.Diff(before, cr.Spec.DeepCopy()); diff != "" {
		t.Errorf("spec was mutated (-want +got):\n%s", diff)
	}
}

// The production shape: one client per reconcile, many reconciles in flight for
// different resources. Run under -race.
func TestConcurrentReconcilesAreIndependent(t *testing.T) {
	allowDescription(t)
	const n = 32
	var wg sync.WaitGroup
	errs := make(chan error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			label := "owner-" + string(rune('a'+i%26))
			r := &remote{description: label, repos: []string{"a"}}
			cr := mr(config(dd.ModeEnabled, ignoreDescription), "seed", "a", "b")

			e := WrapClient(r.client()) // one wrapper per reconcile, as WrapConnector does
			ctx := context.Background()
			if _, err := e.Observe(ctx, cr); err != nil {
				errs <- err
				return
			}
			if _, err := e.Update(ctx, cr); err != nil {
				errs <- err
				return
			}
			if r.description != label {
				t.Errorf("snapshot crossed between reconciles: want %q, got %q", label, r.description)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent reconcile: %v", err)
	}
}

// A mis-wiring that shares one client across reconciles must still be free of
// data races, even though the snapshot semantics would then be wrong. Run under
// -race.
func TestSharedClientIsRaceFree(t *testing.T) {
	allowDescription(t)
	r := &remote{description: "upstream"}
	var mu sync.Mutex
	inner := managed.TypedExternalClientFns[*clusterv1alpha1.Organization]{
		ObserveFn: func(ctx context.Context, mg *clusterv1alpha1.Organization) (managed.ExternalObservation, error) {
			mu.Lock()
			defer mu.Unlock()
			return r.client().Observe(ctx, mg)
		},
		UpdateFn: func(ctx context.Context, mg *clusterv1alpha1.Organization) (managed.ExternalUpdate, error) {
			mu.Lock()
			defer mu.Unlock()
			return r.client().Update(ctx, mg)
		},
		DisconnectFn: func(_ context.Context) error { return nil },
	}
	e := WrapClient[*clusterv1alpha1.Organization](inner)

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cr := mr(config(dd.ModeEnabled, ignoreDescription), "seed")
			ctx := context.Background()
			_, _ = e.Observe(ctx, cr)
			_, _ = e.Update(ctx, cr)
		}()
	}
	wg.Wait()
}

// ─── configuration ───────────────────────────────────────────────────────────

func TestUnobservedIgnoredPathKeepsSeedAndIsReported(t *testing.T) {
	allowDescription(t)
	r := &remote{description: "seed"}
	base := r.client()
	// description is a real, mirrored field -- present under both forProvider
	// and atProvider -- but this fake simulates a server response that omits
	// it, the way a real API sometimes does not echo back every field. No
	// other field drifts, so the unobserved path is the only thing to report.
	client := managed.TypedExternalClientFns[*clusterv1alpha1.Organization]{
		ObserveFn: func(ctx context.Context, mg *clusterv1alpha1.Organization) (managed.ExternalObservation, error) {
			obs, err := base.Observe(ctx, mg)
			mg.Status.AtProvider.Description = ""
			return obs, err
		},
		UpdateFn:     base.Update,
		CreateFn:     base.Create,
		DeleteFn:     base.Delete,
		DisconnectFn: base.Disconnect,
	}
	cr := mr(config(dd.ModeEnabled, ignoreDescription), "seed")

	e := WrapClient[*clusterv1alpha1.Organization](client)
	ctx := context.Background()
	if _, err := e.Observe(ctx, cr); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if _, reason, ok := driftReason(cr); !ok || reason != string(ReasonUnobserved) {
		t.Errorf("want an IgnoredPathUnobserved condition, got %v (present=%v)", reason, ok)
	}
	if _, err := e.Update(ctx, cr); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if r.description != "seed" {
		t.Errorf("want the seed sent when the server reports nothing, got %q", r.description)
	}
}

func TestPathGrammar(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "forProvider.description", want: "forProvider.description"},
		{in: "/forProvider/description", want: "forProvider.description"},
		{in: "forProvider.a.b.c", want: "forProvider.a.b.c"},
		{in: "forProvider.rules[0].ttl", wantErr: true},
		{in: "forProvider.rules[*].ttl", wantErr: true},
		{in: "status.atProvider.description", wantErr: true},
		{in: "", wantErr: true},
	}
	for _, tc := range cases {
		got, err := normalizePath(tc.in)
		switch {
		case tc.wantErr && err == nil:
			t.Errorf("normalizePath(%q): want error, got %q", tc.in, got)
		case !tc.wantErr && err != nil:
			t.Errorf("normalizePath(%q): %v", tc.in, err)
		case !tc.wantErr && got != tc.want:
			t.Errorf("normalizePath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMalformedPathFailsClosed(t *testing.T) {
	r := &remote{description: "upstream"}
	cr := mr(config(dd.ModeEnabled, "forProvider.rules[*].ttl"), "seed")

	e := WrapClient(r.client())
	ctx := context.Background()
	if _, err := e.Observe(ctx, cr); err == nil {
		t.Error("want Observe to fail on an unusable ignore path")
	}
	if _, err := e.Update(ctx, cr); err == nil {
		t.Error("want Update to fail closed on an unusable ignore path")
	}
	if r.updates != 0 {
		t.Errorf("want no write issued, got %d", r.updates)
	}
}

// The mechanism reads configuration off any managed resource without knowing
// its type; a type whose schema has no driftDetection block is unaffected.
func TestReadConfigIsTypeAgnostic(t *testing.T) {
	cfg, err := ReadConfig(&clusterv1alpha1.Organization{})
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}
	if cfg.Mode != ModeEnabled || len(cfg.IgnorePaths) != 0 || cfg.Active() {
		t.Errorf("want inert default config, got %+v", cfg)
	}
}

// A driftDetection block with no ignore paths (Ignore is nil, no omitempty)
// marshals as "ignore":null rather than an absent key. ReadConfig must treat
// that the same as no ignore paths at all, not error out.
func TestReadConfigDriftDetectionWithoutIgnorePaths(t *testing.T) {
	for _, mode := range []dd.Mode{dd.ModeEnabled, dd.ModeWarn, dd.ModeDisabled} {
		cr := mr(config(mode), "mine")
		cfg, err := ReadConfig(cr)
		if err != nil {
			t.Fatalf("ReadConfig(mode=%s): %v", mode, err)
		}
		if string(cfg.Mode) != string(mode) {
			t.Errorf("mode=%s: want Mode %s, got %s", mode, mode, cfg.Mode)
		}
		if len(cfg.IgnorePaths) != 0 {
			t.Errorf("mode=%s: want no ignore paths, got %v", mode, cfg.IgnorePaths)
		}
	}
}

// ─── ignore-path eligibility ─────────────────────────────────────────────────

// The shipped allowlist is empty: this provider's controllers do not mirror
// any forProvider field into status.atProvider, so every ignore path is
// rejected. This pins that fact so an accidental entry -- or the removal of the
// declaration -- shows up as a diff here instead of as a silent policy change.
func TestEligibleIgnorePathsShipsEmpty(t *testing.T) {
	if len(eligibleIgnorePaths) != 0 {
		t.Errorf("want an empty allowlist (no controller populates a forProvider counterpart in status.atProvider), got %v", eligibleIgnorePaths)
	}
}

// Even a field that has a counterpart in the Observation type is rejected when
// the controller never fills it in: it would be observed as absent and the
// update would send the spec value.
func TestUnpopulatedCounterpartPathRejected(t *testing.T) {
	cr := mr(config(dd.ModeEnabled, ignoreDescription), "seed")

	_, err := ReadConfig(cr)
	if err == nil {
		t.Fatal("want ReadConfig to reject a path whose status.atProvider counterpart is never populated")
	}
	if !strings.Contains(err.Error(), errIneligible) {
		t.Errorf("want error to name the ineligibility, got: %v", err)
	}
}

// A write-only field has no counterpart in status.atProvider at all.
func TestWriteOnlyPathRejected(t *testing.T) {
	cr := mr(config(dd.ModeEnabled, "forProvider.actions"), "seed")

	_, err := ReadConfig(cr)
	if err == nil {
		t.Fatal("want ReadConfig to reject a write-only ignore path")
	}
	if !strings.Contains(err.Error(), "status.atProvider") {
		t.Errorf("want error to name the missing observation side, got: %v", err)
	}
}

// A path whose name resolves under status.atProvider but names no field under
// spec.forProvider at all must be rejected outright.
func TestAtProviderOnlyPathRejected(t *testing.T) {
	allowDescription(t)
	cr := mr(config(dd.ModeEnabled, "forProvider.id"), "seed")

	_, err := ReadConfig(cr)
	if err == nil {
		t.Fatal("want ReadConfig to reject a path with no spec.forProvider counterpart")
	}
	if !strings.Contains(err.Error(), errIneligible) {
		t.Errorf("want error to name the ineligibility, got: %v", err)
	}
	if !strings.Contains(err.Error(), "spec.forProvider") {
		t.Errorf("want error to name the missing forProvider side, got: %v", err)
	}
}

// A field that is populated, mirrored on both sides and not identity-bearing is
// accepted once a controller earns its allowlist entry. The gate must not
// over-reject.
func TestEligibleFieldAccepted(t *testing.T) {
	allowDescription(t)
	cr := mr(config(dd.ModeEnabled, ignoreDescription), "seed")

	cfg, err := ReadConfig(cr)
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}
	if len(cfg.IgnorePaths) != 1 || cfg.IgnorePaths[0] != ignoreDescription {
		t.Errorf("want [%s] accepted, got %v", ignoreDescription, cfg.IgnorePaths)
	}
}

// Positional identity in a list is not stable, so an indexed path is rejected
// as unusable grammar rather than reaching the eligibility gate.
func TestIndexedPathRejected(t *testing.T) {
	cr := mr(config(dd.ModeEnabled, "forProvider.actions.enabledRepos[0].repo"), "seed")

	_, err := ReadConfig(cr)
	if err == nil {
		t.Fatal("want ReadConfig to reject an indexed ignore path")
	}
}

// Rejection is total: one ineligible path anywhere in the list fails the
// whole configuration, including entries that were themselves fine. A config
// half-applied from a rejected request is as unreviewable as one silently
// accepted in full.
//
// MUTATION GUARD: if ReadConfig's rejection degrades from "return the error"
// to "skip this entry and continue", this test flips to passing on a config
// that dropped forProvider.actions silently.
func TestIneligiblePathFailsWholeConfig(t *testing.T) {
	allowDescription(t)
	cr := mr(config(dd.ModeEnabled, ignoreDescription, "forProvider.actions"), "seed")

	cfg, err := ReadConfig(cr)
	if err == nil {
		t.Fatalf("want the whole config rejected; got cfg=%+v with no error", cfg)
	}
}

// The identity-participating class is enforced, not just declared: a denylisted
// final segment is rejected even when the path is allowlisted.
func TestIdentityParticipatingPathRejected(t *testing.T) {
	identityDenylist["description"] = true
	defer delete(identityDenylist, "description")
	allowDescription(t)

	cr := mr(config(dd.ModeEnabled, ignoreDescription), "seed")

	_, err := ReadConfig(cr)
	if err == nil {
		t.Fatal("want ReadConfig to reject an identity-participating ignore path")
	}
	if !strings.Contains(err.Error(), "identity-participating") {
		t.Errorf("want error to name the identity-participating class, got: %v", err)
	}
}

// The shipped denylist names the fields GitHub addresses objects by. Pinning it
// makes an accidental removal visible.
func TestIdentityDenylistCoversAddressingFields(t *testing.T) {
	for _, f := range []string{"org", "orgRef", "orgSelector", "url"} {
		if !identityDenylist[f] {
			t.Errorf("want %q in the identity denylist", f)
		}
	}
}

// A wrapped client records what it applied in status.atProvider during Update.
// With an ignored path the update runs on a shadow copy, so the record must be
// carried back to the reconciler's resource, which is the one whose status the
// runtime persists. A failed update records nothing.
func TestUpdateCarriesRecordedStatusBack(t *testing.T) {
	for name, tc := range map[string]struct {
		fail bool
		want string
	}{
		"UpdateSucceeds": {want: "recorded"},
		"UpdateFails":    {fail: true, want: "observed"},
	} {
		t.Run(name, func(t *testing.T) {
			allowDescription(t)
			r := &remote{description: "owned-externally"}
			inner := r.client().(managed.TypedExternalClientFns[*clusterv1alpha1.Organization])
			inner.UpdateFn = func(_ context.Context, mg *clusterv1alpha1.Organization) (managed.ExternalUpdate, error) {
				if tc.fail {
					return managed.ExternalUpdate{}, errors.New("boom")
				}
				mg.Status.AtProvider.Description = "recorded"
				return managed.ExternalUpdate{}, nil
			}
			observe := inner.ObserveFn
			inner.ObserveFn = func(ctx context.Context, mg *clusterv1alpha1.Organization) (managed.ExternalObservation, error) {
				obs, err := observe(ctx, mg)
				mg.Status.AtProvider.Description = "observed"
				return obs, err
			}
			r.description = "observed"
			cr := mr(config(dd.ModeEnabled, ignoreDescription), "seed")

			e := WrapClient[*clusterv1alpha1.Organization](inner)
			ctx := context.Background()
			if _, err := e.Observe(ctx, cr); err != nil {
				t.Fatalf("Observe: %v", err)
			}
			_, err := e.Update(ctx, cr)
			if (err != nil) != tc.fail {
				t.Fatalf("Update error = %v, want failure %v", err, tc.fail)
			}
			if got := cr.Status.AtProvider.Description; got != tc.want {
				t.Errorf("status.atProvider.description = %q, want %q", got, tc.want)
			}
		})
	}
}
