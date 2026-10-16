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

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-github/v90/github"

	clusterv1alpha1 "github.com/provider-github/provider-github/apis/cluster/organizations/v1alpha1"
	namespacedv1alpha1 "github.com/provider-github/provider-github/apis/namespaced/organizations/v1alpha1"
	"github.com/provider-github/provider-github/internal/clients/fake"
	"github.com/provider-github/provider-github/internal/clients/rulesets"
	"github.com/provider-github/provider-github/internal/controller/scopebridge"
	"github.com/provider-github/provider-github/internal/driftdetection/ddtest"
)

// rulesetScopes are the two scope variants of the Repository kind. Each runs the same
// reconciler: the cluster kind directly, the namespaced kind through the scope bridge.
var rulesetScopes = []string{"Cluster", "Namespaced"}

// scopedRepository drives one scope variant of the Repository reconciler over a cluster
// Repository fixture, and reads the result back the same way for both scopes.
type scopedRepository struct {
	observe   func() (managed.ExternalObservation, error)
	update    func() error
	condition func(xpv2.ConditionType) xpv2.Condition
	// atProviderRules is status.atProvider.repositoryRules as JSON.
	atProviderRules func() []byte
}

func newScopedRepository(t *testing.T, scope string, e *external, cr *clusterv1alpha1.Repository) scopedRepository {
	t.Helper()
	ctx := context.Background()
	if scope == "Cluster" {
		return scopedRepository{
			observe:   func() (managed.ExternalObservation, error) { return e.Observe(ctx, cr) },
			update:    func() error { _, err := e.Update(ctx, cr); return err },
			condition: cr.GetCondition,
			atProviderRules: func() []byte {
				return marshalJSON(t, cr.Status.AtProvider.RepositoryRules)
			},
		}
	}
	ns := ddtest.Convert(t, cr, &namespacedv1alpha1.Repository{})
	ns.Namespace = "default"
	bridge := scopebridge.New[*namespacedv1alpha1.Repository](e,
		func() *clusterv1alpha1.Repository { return &clusterv1alpha1.Repository{} })
	return scopedRepository{
		observe:   func() (managed.ExternalObservation, error) { return bridge.Observe(ctx, ns) },
		update:    func() error { _, err := bridge.Update(ctx, ns); return err },
		condition: ns.GetCondition,
		atProviderRules: func() []byte {
			return marshalJSON(t, ns.Status.AtProvider.RepositoryRules)
		},
	}
}

func marshalJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return b
}

// settingsWritesAllowed lets an Update reach the ruleset step: the repository settings it writes first are echoed.
func settingsWritesAllowed() *fake.MockRepositoriesClient {
	repos := upToDateRepositories(nil)
	repos.MockEdit = func(_ context.Context, _, _ string, req *github.Repository) (*github.Repository, *github.Response, error) {
		return req, fake.GenerateEmptyResponse(), nil
	}
	repos.MockReplaceAllTopics = func(_ context.Context, _, _ string, topics []string) ([]string, *github.Response, error) {
		return topics, fake.GenerateEmptyResponse(), nil
	}
	return repos
}

// subsetMissing lists the paths of want that got does not hold. Maps are compared by key,
// lists by position, and an empty list or an absent value in want asks for nothing.
func subsetMissing(want, got any, path string) []string {
	switch w := want.(type) {
	case nil:
		return nil
	case map[string]any:
		g, _ := got.(map[string]any)
		var missing []string
		for _, k := range slices.Sorted(func(yield func(string) bool) {
			for k := range w {
				if !yield(k) {
					return
				}
			}
		}) {
			missing = append(missing, subsetMissing(w[k], g[k], path+"."+k)...)
		}
		return missing
	case []any:
		if len(w) == 0 {
			return nil
		}
		g, _ := got.([]any)
		if len(g) != len(w) {
			return []string{fmt.Sprintf("%s: %d elements, want %d", path, len(g), len(w))}
		}
		var missing []string
		for i := range w {
			missing = append(missing, subsetMissing(w[i], g[i], fmt.Sprintf("%s[%d]", path, i))...)
		}
		return missing
	default:
		if !reflect.DeepEqual(want, got) {
			return []string{fmt.Sprintf("%s: %v, want %v", path, got, want)}
		}
		return nil
	}
}

func decodeJSON(t *testing.T, b []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	return v
}

// Every modelled rule behaves the same in both scopes: GitHub holding what the spec declares
// is up to date and RulesetsPartial is False; a real difference is drift, shown as Ready
// False, and the update sends the declared rule; and the rule is mirrored into
// status.atProvider with nothing dropped.
func TestRulesetRulesInBothScopes(t *testing.T) {
	for typ, tc := range modelledRuleCases() {
		for _, scope := range rulesetScopes {
			t.Run(scope+"/"+typ, func(t *testing.T) {
				cr := repository(withRules(tc.cr))
				rs := upToDateRulesets()
				rulesetHolding(t, rs, tc.onGitHub[0])
				s := newScopedRepository(t, scope, &external{github: clientFor(upToDateRepositories(nil), rs)}, cr)

				got, err := s.observe()
				if err != nil {
					t.Fatalf("Observe: %v", err)
				}
				if !got.ResourceExists || !got.ResourceUpToDate {
					t.Fatalf("Observe = %+v, want an existing, up to date repository: GitHub holds what the spec declares", got)
				}
				if c := s.condition(typeRulesetsPartial); c.Status != corev1.ConditionFalse || c.Reason != reasonRulesetsReconciled {
					t.Errorf("RulesetsPartial = %s/%s, want False/%s", c.Status, c.Reason, reasonRulesetsReconciled)
				}

				// The spec's ruleset, as compared, is a subset of what status.atProvider holds.
				want := crRulesets(t, *cr.Spec.ForProvider.RepositoryRules)[rr1name]
				mirrored := decodeJSON(t, s.atProviderRules())
				list, _ := mirrored.([]any)
				if len(list) != 1 {
					t.Fatalf("status.atProvider.repositoryRules holds %d rulesets, want 1: %s", len(list), s.atProviderRules())
				}
				if missing := subsetMissing(decodeJSON(t, marshalJSON(t, want)), list[0], "repositoryRules[0]"); len(missing) > 0 {
					t.Errorf("status.atProvider.repositoryRules drops what GitHub returned:\n%s", strings.Join(missing, "\n"))
				}
			})
		}
	}
}

// A difference in a modelled rule is drift in both scopes, Ready turns False, and Update
// sends the declared rule in both.
func TestRulesetDriftInBothScopes(t *testing.T) {
	for typ, tc := range modelledRuleCases() {
		for field, change := range tc.differs {
			for _, scope := range rulesetScopes {
				t.Run(scope+"/"+typ+"/"+field, func(t *testing.T) {
					rs := upToDateRulesets()
					rulesetHolding(t, rs, tc.onGitHub[0], change)
					var sent [][]*rulesets.Rule
					rs.MockUpdateRuleset = func(_ context.Context, _, _ string, _ int64, r rulesets.Ruleset) (*rulesets.Ruleset, *github.Response, error) {
						sent = append(sent, r.Rules)
						return &r, fake.GenerateEmptyResponse(), nil
					}

					cr := repository(withRules(tc.cr))
					s := newScopedRepository(t, scope, &external{github: clientFor(upToDateRepositories(nil), rs)}, cr)
					got, err := s.observe()
					if err != nil {
						t.Fatalf("Observe: %v", err)
					}
					if !got.ResourceExists || got.ResourceUpToDate {
						t.Fatalf("Observe = %+v, want an existing repository that is not up to date", got)
					}
					assertUnavailable(t, s.condition(xpv2.TypeReady))

					upd := settingsOnlyRepository()
					upd.Spec.ForProvider.RepositoryRules = cr.Spec.ForProvider.RepositoryRules
					u := newScopedRepository(t, scope, &external{github: clientFor(settingsWritesAllowed(), rs)}, upd)
					if err := u.update(); err != nil {
						t.Fatalf("Update: %v", err)
					}
					if len(sent) != 1 {
						t.Fatalf("UpdateRuleset called %d times, want 1", len(sent))
					}
					decoded, err := rulesets.Decode(sent[0])
					if err != nil {
						t.Fatalf("decode sent rules: %v", err)
					}
					want := githubRules()
					tc.sent(want)
					if diff := cmp.Diff(want, decoded); diff != "" {
						t.Errorf("UpdateRuleset rules: -want, +got:\n%s", diff)
					}
				})
			}
		}
	}
}

// The RulesetsPartial conditions and their reasons are the same in both scopes.
func TestRulesetsPartialInBothScopes(t *testing.T) {
	duplicateRef := func() *clusterv1alpha1.Repository {
		cr := repository()
		(*cr.Spec.ForProvider.RepositoryRules)[0].Conditions.RefName.Include = []string{"~ALL", "~ALL"}
		return cr
	}
	unmanagedType := func() (*clusterv1alpha1.Repository, *fake.MockRulesetsClient) {
		return repository(), twoRulesetsWithUnmanagedType()
	}
	cases := map[string]struct {
		cr         func() (*clusterv1alpha1.Repository, *fake.MockRulesetsClient)
		wantStatus corev1.ConditionStatus
		wantReason xpv2.ConditionReason
		wantExists bool
	}{
		"Reconciled": {
			cr: func() (*clusterv1alpha1.Repository, *fake.MockRulesetsClient) {
				return repository(), upToDateRulesets()
			},
			wantStatus: corev1.ConditionFalse,
			wantReason: reasonRulesetsReconciled,
		},
		"InvalidRulesets": {
			cr: func() (*clusterv1alpha1.Repository, *fake.MockRulesetsClient) {
				return duplicateRef(), rulesetsWithoutCalls(t)
			},
			wantStatus: corev1.ConditionTrue,
			wantReason: reasonInvalidRulesets,
		},
		"RulesetsForbidden": {
			cr: func() (*clusterv1alpha1.Repository, *fake.MockRulesetsClient) {
				return repository(), rulesetsForbidden(t)
			},
			wantStatus: corev1.ConditionTrue,
			wantReason: reasonRulesetsForbidden,
		},
		"UnmanagedRuleTypes": {
			cr:         unmanagedType,
			wantStatus: corev1.ConditionTrue,
			wantReason: reasonUnmanagedRuleTypes,
		},
	}
	for name, tc := range cases {
		for _, scope := range rulesetScopes {
			t.Run(scope+"/"+name, func(t *testing.T) {
				cr, rs := tc.cr()
				s := newScopedRepository(t, scope, &external{github: clientFor(upToDateRepositories(nil), rs)}, cr)
				got, err := s.observe()
				if err != nil {
					t.Fatalf("Observe: %v", err)
				}
				if !got.ResourceExists || !got.ResourceUpToDate {
					t.Errorf("Observe = %+v, want an up to date repository: the rulesets that cannot be reconciled do not hold it back", got)
				}
				if c := s.condition(typeRulesetsPartial); c.Status != tc.wantStatus || c.Reason != tc.wantReason {
					t.Errorf("RulesetsPartial = %s/%s, want %s/%s", c.Status, c.Reason, tc.wantStatus, tc.wantReason)
				}
			})
		}
	}
}

// twoRulesetsWithUnmanagedType returns the declared ruleset holding a rule type the provider
// does not model.
func twoRulesetsWithUnmanagedType() *fake.MockRulesetsClient {
	rs := upToDateRulesets()
	rs.MockGetRuleset = func(_ context.Context, _, _ string, _ int64, _ bool) (*rulesets.Ruleset, *github.Response, error) {
		r := githubRuleset()[0]
		withRuleTypes("repository_create")(r)
		return r, fake.GenerateEmptyResponse(), nil
	}
	return rs
}

// An empty repositoryRules deletes every ruleset of the repository, and an unset one leaves
// them, in both scopes.
func TestEmptyRepositoryRulesInBothScopes(t *testing.T) {
	for _, scope := range rulesetScopes {
		t.Run(scope+"/Empty", func(t *testing.T) {
			rs := upToDateRulesets()
			var deleted []int64
			rs.MockDeleteRuleset = func(_ context.Context, _, _ string, id int64) (*github.Response, error) {
				deleted = append(deleted, id)
				return fake.GenerateEmptyResponse(), nil
			}
			cr := settingsOnlyRepository()
			cr.Spec.ForProvider.RepositoryRules = &[]clusterv1alpha1.RepositoryRuleset{}

			s := newScopedRepository(t, scope, &external{github: clientFor(settingsWritesAllowed(), rs)}, cr)
			got, err := s.observe()
			if err != nil {
				t.Fatalf("Observe: %v", err)
			}
			if got.ResourceUpToDate {
				t.Errorf("ResourceUpToDate = true, want false: GitHub holds a ruleset the spec does not declare")
			}
			if err := s.update(); err != nil {
				t.Fatalf("Update: %v", err)
			}
			if diff := cmp.Diff([]int64{rr1Id}, deleted); diff != "" {
				t.Errorf("deleted ruleset IDs: -want, +got:\n%s", diff)
			}
		})
		t.Run(scope+"/Unset", func(t *testing.T) {
			cr := repository()
			cr.Spec.ForProvider.RepositoryRules = nil
			s := newScopedRepository(t, scope, &external{github: clientFor(upToDateRepositories(nil), rulesetsWithoutCalls(t))}, cr)
			got, err := s.observe()
			if err != nil {
				t.Fatalf("Observe: %v", err)
			}
			if !got.ResourceUpToDate {
				t.Errorf("ResourceUpToDate = false, want true: no repositoryRules means the rulesets on GitHub are left as they are")
			}
			u := newScopedRepository(t, scope, &external{github: clientFor(settingsWritesAllowed(), rulesetsWithoutCalls(t))}, settingsOnlyRepository())
			if err := u.update(); err != nil {
				t.Fatalf("Update: %v", err)
			}
		})
	}
}

// fullRuleset declares every rule type and parameter the provider models, in a push-free
// branch ruleset, with values that differ from every default.
func fullRuleset() clusterv1alpha1.RepositoryRuleset {
	tr := github.Ptr(true)
	pattern := func(op, p string) *clusterv1alpha1.RulesPattern {
		return &clusterv1alpha1.RulesPattern{Name: github.Ptr("rule " + p), Negate: tr, Operator: op, Pattern: p}
	}
	return clusterv1alpha1.RepositoryRuleset{
		Name:        rr1name,
		Enforcement: github.Ptr("evaluate"),
		Target:      github.Ptr("branch"),
		BypassActors: []*clusterv1alpha1.RulesetByPassActors{
			{ActorID: github.Ptr(int64(5)), ActorType: github.Ptr("RepositoryRole"), BypassMode: github.Ptr("pull_request")},
			{ActorType: github.Ptr("OrganizationAdmin"), BypassMode: github.Ptr("always")},
		},
		Conditions: &clusterv1alpha1.RulesetConditions{RefName: &clusterv1alpha1.RulesetRefName{
			Include: []string{"refs/heads/main", "refs/heads/release/*"},
			Exclude: []string{"refs/heads/tmp/*"},
		}},
		Rules: &clusterv1alpha1.Rules{
			Creation:                  tr,
			Update:                    tr,
			UpdateAllowsFetchAndMerge: tr,
			Deletion:                  tr,
			RequiredLinearHistory:     tr,
			RequiredSignatures:        tr,
			NonFastForward:            tr,
			LicenseComplianceScanning: tr,
			RequiredDeployments:       &clusterv1alpha1.RulesRequiredDeployments{Environments: []string{"staging", "production"}},
			PullRequest: &clusterv1alpha1.RulesPullRequest{
				AllowedMergeMethods: []clusterv1alpha1.RulesMergeMethod{"squash", "rebase"},
				DismissalRestriction: &clusterv1alpha1.RulesDismissalRestriction{Enabled: true, AllowedActors: []*clusterv1alpha1.RulesDismissalActor{
					{ID: 3, Type: "User"}, {ID: 9, Type: "Team"},
				}},
				DismissStaleReviewsOnPush:                  tr,
				RequireExtraApprovalForUnattributedChanges: github.Ptr(false),
				RequiredReviewers: []*clusterv1alpha1.RulesRequiredReviewer{
					{FilePatterns: []string{"*.go", "!vendor/**"}, MinimumApprovals: 2, Reviewer: clusterv1alpha1.RulesReviewer{ID: 7, Type: "Team"}},
				},
				RequireCodeOwnerReview:         tr,
				RequireLastPushApproval:        tr,
				RequiredApprovingReviewCount:   github.Ptr(3),
				RequiredReviewThreadResolution: tr,
			},
			RequiredStatusChecks: &clusterv1alpha1.RulesRequiredStatusChecks{
				DoNotEnforceOnCreate:             tr,
				StrictRequiredStatusChecksPolicy: tr,
				RequiredStatusChecks: []*clusterv1alpha1.RulesRequiredStatusChecksParameters{
					{Context: "ci", IntegrationID: github.Ptr(int64(15368))},
					{Context: "lint"},
				},
			},
			MergeQueue: &clusterv1alpha1.RulesMergeQueue{
				CheckResponseTimeoutMinutes: 60, GroupingStrategy: "HEADGREEN", MaxEntriesToBuild: 8, MaxEntriesToMerge: 4,
				MergeMethod: "SQUASH", MinEntriesToMerge: 2, MinEntriesToMergeWaitMinutes: 5,
			},
			CommitMessagePattern:     pattern("starts_with", "feat"),
			CommitAuthorEmailPattern: pattern("ends_with", "@example.com"),
			CommitterEmailPattern:    pattern("contains", "bot"),
			BranchNamePattern:        pattern("regex", "^[a-z]+$"),
			TagNamePattern:           pattern("regex", "^v[0-9]+$"),
			CodeScanning: &clusterv1alpha1.RulesCodeScanning{CodeScanningTools: []*clusterv1alpha1.RulesCodeScanningTool{
				{Tool: "CodeQL", AlertsThreshold: "errors", SecurityAlertsThreshold: "high_or_higher"},
			}},
			CodeQuality:                          &clusterv1alpha1.RulesCodeQuality{Severity: "warnings"},
			CodeCoverage:                         &clusterv1alpha1.RulesCodeCoverage{MinimumCoverage: github.Ptr("80"), MaxCoverageDrop: github.Ptr("2.5")},
			RequireSecretScanningAlertResolution: &clusterv1alpha1.RulesSecretScanningAlertResolution{SecretTypes: []clusterv1alpha1.RulesSecretType{"custom_patterns", "provider_patterns"}},
			CopilotCodeReview:                    &clusterv1alpha1.RulesCopilotCodeReview{ReviewOnPush: tr, ReviewDraftPullRequests: tr},
		},
	}
}

// fullPushRuleset declares the rule types only a push ruleset takes.
func fullPushRuleset() clusterv1alpha1.RepositoryRuleset {
	return clusterv1alpha1.RepositoryRuleset{
		Name:   "push-rules",
		Target: github.Ptr("push"),
		Rules: &clusterv1alpha1.Rules{
			FileExtensionRestriction: &clusterv1alpha1.RulesFileExtensionRestriction{RestrictedFileExtensions: []string{"*.exe", "*.zip"}},
			FilePathRestriction: &clusterv1alpha1.RulesFilePathRestriction{
				RestrictedFilePaths: []string{"secrets/**"}, IgnoredFilePaths: []string{"secrets/README.md"},
			},
			MaxFilePathLength: &clusterv1alpha1.RulesMaxFilePathLength{MaxFilePathLength: 255},
			MaxFileSize:       &clusterv1alpha1.RulesMaxFileSize{MaxFileSize: 50, IgnoredFilePaths: []string{"assets/**"}},
		},
	}
}

// githubHolding returns the rulesets GitHub holds for the declared ones: each declared
// ruleset as the provider would send it, as a response.
func githubHolding(t *testing.T, declared ...clusterv1alpha1.RepositoryRuleset) *fake.MockRulesetsClient {
	t.Helper()
	held := make([]*rulesets.Ruleset, len(declared))
	byID := map[int64]*rulesets.Ruleset{}
	for i, d := range declared {
		sent, err := crRepoRulesToRulesConfig(d)
		if err != nil {
			t.Fatalf("crRepoRulesToRulesConfig(%s): %v", d.Name, err)
		}
		r := *sent
		r.ID = github.Ptr(int64(100 + i))
		r.SourceType = github.Ptr("Repository")
		held[i] = &r
		byID[*r.ID] = &r
	}
	return &fake.MockRulesetsClient{
		MockGetAllRulesets: func(context.Context, string, string, *github.RepositoryListRulesetsOptions) ([]*rulesets.Ruleset, *github.Response, error) {
			return held, fake.GenerateEmptyResponse(), nil
		},
		MockGetRuleset: func(_ context.Context, _, _ string, id int64, _ bool) (*rulesets.Ruleset, *github.Response, error) {
			r := *byID[id]
			return &r, fake.GenerateEmptyResponse(), nil
		},
	}
}

// A ruleset holding every rule type and parameter GitHub returns is in sync with the spec
// declaring them, and status.atProvider mirrors every one of them, in both scopes. A field the
// observation lacked would be dropped without an error, since the mirror goes through JSON.
func TestFullRulesetRoundTripsIntoAtProviderInBothScopes(t *testing.T) {
	for _, scope := range rulesetScopes {
		t.Run(scope, func(t *testing.T) {
			declared := []clusterv1alpha1.RepositoryRuleset{fullRuleset(), fullPushRuleset()}
			cr := repository()
			cr.Spec.ForProvider.RepositoryRules = &declared
			s := newScopedRepository(t, scope, &external{github: clientFor(upToDateRepositories(nil), githubHolding(t, declared...))}, cr)

			got, err := s.observe()
			if err != nil {
				t.Fatalf("Observe: %v", err)
			}
			if !got.ResourceUpToDate {
				t.Fatalf("Observe = %+v, want up to date: GitHub holds exactly what the spec declares", got)
			}
			if c := s.condition(typeRulesetsPartial); c.Status != corev1.ConditionFalse {
				t.Errorf("RulesetsPartial = %s/%s (%s), want False", c.Status, c.Reason, c.Message)
			}

			list, _ := decodeJSON(t, s.atProviderRules()).([]any)
			if len(list) != len(declared) {
				t.Fatalf("status.atProvider.repositoryRules holds %d rulesets, want %d: %s", len(list), len(declared), s.atProviderRules())
			}
			want := crRulesets(t, declared)
			for i, name := range []string{"push-rules", rr1name} {
				observed, _ := list[i].(map[string]any)
				if observed["name"] != name {
					t.Fatalf("status.atProvider.repositoryRules[%d] is %v, want %s (sorted by name)", i, observed["name"], name)
				}
				// bypassActors are compared by their type, mode and ID; the rest by every key.
				if missing := subsetMissing(decodeJSON(t, marshalJSON(t, want[name])), observed, name); len(missing) > 0 {
					t.Errorf("status.atProvider.repositoryRules drops what GitHub returned:\n%s", strings.Join(missing, "\n"))
				}
			}
		})
	}
}

// jsonFields returns the JSON object keys of struct type t and the field types they name.
func jsonFields(t reflect.Type) map[string]reflect.Type {
	fields := map[string]reflect.Type{}
	for f := range t.Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		fields[name] = f.Type
	}
	return fields
}

// element strips pointers and list wrappers from a field type.
func element(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice {
		t = t.Elem()
	}
	return t
}

// requireObserved fails for every field of the spec type that its observation twin lacks, and
// follows the struct-valued fields into their own twins.
func requireObserved(t *testing.T, spec, observed reflect.Type, path string) {
	t.Helper()
	have := jsonFields(observed)
	for name, field := range jsonFields(spec) {
		twin, ok := have[name]
		if !ok {
			t.Errorf("%s.%s is in the spec but not in %s, so the mirror drops it", path, name, observed.Name())
			continue
		}
		if s, o := element(field), element(twin); s.Kind() == reflect.Struct && o.Kind() == reflect.Struct {
			requireObserved(t, s, o, path+"."+name)
		}
	}
}

// Every field of a ruleset in the spec has a twin in status.atProvider, in both scopes. Adding
// a rule type or parameter to the spec without its observation twin fails here.
func TestRepositoryRulesetObservationMirrorsEverySpecField(t *testing.T) {
	t.Run("Cluster", func(t *testing.T) {
		requireObserved(t, reflect.TypeFor[clusterv1alpha1.RepositoryRuleset](), reflect.TypeFor[clusterv1alpha1.RepositoryRulesetObservation](), "repositoryRules")
	})
	t.Run("Namespaced", func(t *testing.T) {
		requireObserved(t, reflect.TypeFor[namespacedv1alpha1.RepositoryRuleset](), reflect.TypeFor[namespacedv1alpha1.RepositoryRulesetObservation](), "repositoryRules")
	})
}

// A ruleset with a rule the provider models is dropped from nothing when the repository is
// archived: the frozen section is read and mirrored in both scopes, and does not take part
// in drift.
func TestArchivedRepositoryMirrorsRulesetsInBothScopes(t *testing.T) {
	for _, scope := range rulesetScopes {
		t.Run(scope, func(t *testing.T) {
			declared := []clusterv1alpha1.RepositoryRuleset{fullRuleset()}
			cr := repository(withArchived(true))
			cr.Spec.ForProvider.RepositoryRules = &declared
			rs := githubHolding(t, declared...)
			// A ruleset the spec does not name is mirrored as well: nothing is deleted while archived.
			held, _, _ := rs.MockGetAllRulesets(context.Background(), "", "", nil)
			other := *held[0]
			other.ID = github.Ptr(int64(200))
			other.Name = "undeclared"
			held = append(held, &other)
			rs.MockGetAllRulesets = func(context.Context, string, string, *github.RepositoryListRulesetsOptions) ([]*rulesets.Ruleset, *github.Response, error) {
				return held, fake.GenerateEmptyResponse(), nil
			}
			getRuleset := rs.MockGetRuleset
			rs.MockGetRuleset = func(ctx context.Context, owner, repo string, id int64, parents bool) (*rulesets.Ruleset, *github.Response, error) {
				if id == 200 {
					o := other
					return &o, fake.GenerateEmptyResponse(), nil
				}
				return getRuleset(ctx, owner, repo, id, parents)
			}

			s := newScopedRepository(t, scope, &external{github: clientFor(archivedRepositories(), rs)}, cr)
			if _, err := s.observe(); err != nil {
				t.Fatalf("Observe: %v", err)
			}
			list, _ := decodeJSON(t, s.atProviderRules()).([]any)
			if len(list) != 2 {
				t.Fatalf("status.atProvider.repositoryRules holds %d rulesets, want 2: %s", len(list), s.atProviderRules())
			}
			want := crRulesets(t, declared)[rr1name]
			var found bool
			for _, item := range list {
				if m, _ := item.(map[string]any); m["name"] == rr1name {
					found = true
					if missing := subsetMissing(decodeJSON(t, marshalJSON(t, want)), item, rr1name); len(missing) > 0 {
						t.Errorf("archived: status.atProvider.repositoryRules drops what GitHub returned:\n%s", strings.Join(missing, "\n"))
					}
				}
			}
			if !found {
				t.Errorf("status.atProvider.repositoryRules lacks %s: %s", rr1name, s.atProviderRules())
			}
		})
	}
}
