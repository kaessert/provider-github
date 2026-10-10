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

package secretaccess

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/google/go-github/v90/github"
	corev1 "k8s.io/api/core/v1"

	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	"github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
)

var ignoreTransitionTime = cmpopts.IgnoreFields(xpv2.Condition{}, "LastTransitionTime")

func specRepos(names ...string) []v1alpha1.SecretSelectedRepo {
	out := make([]v1alpha1.SecretSelectedRepo, 0, len(names))
	for _, n := range names {
		out = append(out, v1alpha1.SecretSelectedRepo{Repo: n})
	}
	return out
}

func TestNotReady(t *testing.T) {
	cases := map[string]struct {
		reason  xpv2.ConditionReason
		message string
	}{
		"CustomReason": {reason: "Anything", message: "some message"},
		"EmptyMessage": {reason: reasonSecretNotFound, message: ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := notReady(tc.reason, tc.message)
			want := xpv2.Condition{
				Type:    xpv2.TypeReady,
				Status:  corev1.ConditionFalse,
				Reason:  tc.reason,
				Message: tc.message,
			}
			if diff := cmp.Diff(want, got, ignoreTransitionTime); diff != "" {
				t.Errorf("notReady(...): -want, +got:\n%s", diff)
			}
			if got.LastTransitionTime.IsZero() {
				t.Error("notReady(...): LastTransitionTime must be set")
			}
		})
	}
}

func TestSecretNotFound(t *testing.T) {
	got := SecretNotFound("MY_SECRET")
	want := xpv2.Condition{
		Type:    xpv2.TypeReady,
		Status:  corev1.ConditionFalse,
		Reason:  "SecretNotFound",
		Message: "secret MY_SECRET not found on GitHub; create it there first (this resource only manages repository access)",
	}
	if diff := cmp.Diff(want, got, ignoreTransitionTime); diff != "" {
		t.Errorf("SecretNotFound(...): -want, +got:\n%s", diff)
	}
}

func TestVisibilityMismatch(t *testing.T) {
	cases := map[string]struct {
		gh, spec string
		message  string
	}{
		"GitHubAllSpecSelected": {
			gh: "all", spec: "selected",
			message: `visibility is "all" on GitHub but "selected" in spec; change it on GitHub (the API cannot change visibility without the secret value)`,
		},
		"GitHubPrivateSpecAll": {
			gh: "private", spec: "all",
			message: `visibility is "private" on GitHub but "all" in spec; change it on GitHub (the API cannot change visibility without the secret value)`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := VisibilityMismatch(tc.gh, tc.spec)
			want := xpv2.Condition{
				Type:    xpv2.TypeReady,
				Status:  corev1.ConditionFalse,
				Reason:  "VisibilityMismatch",
				Message: tc.message,
			}
			if diff := cmp.Diff(want, got, ignoreTransitionTime); diff != "" {
				t.Errorf("VisibilityMismatch(...): -want, +got:\n%s", diff)
			}
		})
	}
}

func TestSecretNotFoundError(t *testing.T) {
	err := SecretNotFoundError("MY_SECRET")
	if err == nil {
		t.Fatal("SecretNotFoundError(...): want error, got nil")
	}
	if got, want := err.Error(), "secret MY_SECRET not found on GitHub; create it there first"; got != want {
		t.Errorf("SecretNotFoundError(...): want %q, got %q", want, got)
	}
}

func TestRepoNames(t *testing.T) {
	cases := map[string]struct {
		reason string
		refs   []v1alpha1.SecretSelectedRepo
		want   []string
	}{
		"Nil":             {reason: "No refs yields an empty, non-nil list.", refs: nil, want: []string{}},
		"Order":           {reason: "Spec order is kept.", refs: specRepos("b", "a", "c"), want: []string{"b", "a", "c"}},
		"ExactDuplicate":  {reason: "An exact duplicate is dropped.", refs: specRepos("a", "b", "a"), want: []string{"a", "b"}},
		"CaseInsensitive": {reason: "The first spelling wins among case variants.", refs: specRepos("Repo", "repo", "REPO"), want: []string{"Repo"}},
		"EmptyNameKept":   {reason: "An empty name is not special-cased.", refs: specRepos(""), want: []string{""}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := RepoNames(tc.refs)
			if got == nil {
				t.Fatalf("RepoNames(...): got nil, want non-nil\n%s", tc.reason)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("RepoNames(...): -want, +got:\n%s\n%s", diff, tc.reason)
			}
		})
	}
}

func TestSameRepos(t *testing.T) {
	cases := map[string]struct {
		reason  string
		spec    []v1alpha1.SecretSelectedRepo
		ghNames []string
		want    bool
	}{
		"BothEmpty":           {reason: "Nothing on either side is equal.", spec: nil, ghNames: nil, want: true},
		"SameOrder":           {reason: "Identical lists match.", spec: specRepos("a", "b"), ghNames: []string{"a", "b"}, want: true},
		"DifferentOrder":      {reason: "Order is ignored.", spec: specRepos("a", "b"), ghNames: []string{"b", "a"}, want: true},
		"DifferentCase":       {reason: "Case is ignored.", spec: specRepos("Repo-A"), ghNames: []string{"repo-a"}, want: true},
		"GitHubCanonicalCase": {reason: "GitHub reports the canonical spelling while the spec is lower case; that is not drift.", spec: specRepos("repo-a"), ghNames: []string{"Repo-A"}, want: true},
		"SpecDuplicates":      {reason: "Spec duplicates collapse, so they are not drift.", spec: specRepos("a", "A", "b"), ghNames: []string{"a", "b"}, want: true},
		"SpecHasExtra":        {reason: "A repository missing on GitHub is drift.", spec: specRepos("a", "b"), ghNames: []string{"a"}, want: false},
		"GitHubHasExtra":      {reason: "A repository only on GitHub is drift.", spec: specRepos("a"), ghNames: []string{"a", "b"}, want: false},
		"DifferentNames":      {reason: "Same length, different members.", spec: specRepos("a"), ghNames: []string{"b"}, want: false},
		"EmptySpecNonEmpty":   {reason: "Empty spec against a populated list is drift.", spec: nil, ghNames: []string{"a"}, want: false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := SameRepos(tc.spec, tc.ghNames); got != tc.want {
				t.Errorf("SameRepos(...): want %t, got %t\n%s", tc.want, got, tc.reason)
			}
		})
	}
}

func TestSameReposDoesNotMutateInputs(t *testing.T) {
	spec := specRepos("B", "a")
	gh := []string{"B", "A"}
	_ = SameRepos(spec, gh)
	if diff := cmp.Diff(specRepos("B", "a"), spec); diff != "" {
		t.Errorf("SameRepos mutated spec: -want, +got:\n%s", diff)
	}
	if diff := cmp.Diff([]string{"B", "A"}, gh); diff != "" {
		t.Errorf("SameRepos mutated ghNames: -want, +got:\n%s", diff)
	}
}

func TestRepoObservations(t *testing.T) {
	cases := map[string]struct {
		names []string
		want  []v1alpha1.SecretSelectedRepoObservation
	}{
		"Nil":      {names: nil, want: []v1alpha1.SecretSelectedRepoObservation{}},
		"Sorted":   {names: []string{"c", "a", "b"}, want: []v1alpha1.SecretSelectedRepoObservation{{Repo: "a"}, {Repo: "b"}, {Repo: "c"}}},
		"Single":   {names: []string{"only"}, want: []v1alpha1.SecretSelectedRepoObservation{{Repo: "only"}}},
		"CaseSort": {names: []string{"b", "B", "a"}, want: []v1alpha1.SecretSelectedRepoObservation{{Repo: "B"}, {Repo: "a"}, {Repo: "b"}}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := RepoObservations(tc.names)
			if got == nil {
				t.Fatal("RepoObservations(...): got nil, want non-nil")
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("RepoObservations(...): -want, +got:\n%s", diff)
			}
		})
	}
}

func TestRenamedRepoError(t *testing.T) {
	cases := map[string]struct {
		reason   string
		known    map[string]int64
		added    []string
		addedIDs []int64
		wantErr  string
	}{
		"NothingAdded": {reason: "No added names, no error.", known: map[string]int64{"a": 1}},
		"NewRepo": {
			reason: "An added ID unknown to the list is a genuinely new repo.",
			known:  map[string]int64{"a": 1}, added: []string{"b"}, addedIDs: []int64{2},
		},
		"Renamed": {
			reason: "An added name resolving to a known ID was renamed.",
			known:  map[string]int64{"new-name": 7}, added: []string{"old-name"}, addedIDs: []int64{7},
			wantErr: "repository old-name is named new-name on GitHub; update selectedRepositories to the new name",
		},
		"RenamedSecondOfMany": {
			reason: "The renamed entry is found past a new one.",
			known:  map[string]int64{"new-name": 7}, added: []string{"fresh", "old-name"}, addedIDs: []int64{9, 7},
			wantErr: "repository old-name is named new-name on GitHub; update selectedRepositories to the new name",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := RenamedRepoError(tc.known, tc.added, tc.addedIDs)
			if tc.wantErr == "" {
				if err != nil {
					t.Errorf("RenamedRepoError(...): want nil, got %v\n%s", err, tc.reason)
				}
				return
			}
			if err == nil || err.Error() != tc.wantErr {
				t.Errorf("RenamedRepoError(...): want %q, got %v\n%s", tc.wantErr, err, tc.reason)
			}
		})
	}
}

// fakeLister serves pages of repositories keyed by page number (0 and 1 are
// the first page) and records the options it was called with.
type fakeLister struct {
	pages   map[int][]*github.Repository
	err     error
	errPage int
	calls   []github.ListOptions
}

func (f *fakeLister) ListSelectedReposForOrgSecret(_ context.Context, org, name string, opts *github.ListOptions) (*github.SelectedReposList, *github.Response, error) {
	f.calls = append(f.calls, *opts)
	if org != "org" || name != "SECRET" {
		return nil, nil, errors.New("unexpected org/secret: " + org + "/" + name)
	}
	if f.err != nil && opts.Page == f.errPage {
		return nil, nil, f.err
	}
	page := opts.Page
	if page == 0 {
		page = 1
	}
	next := 0
	if _, ok := f.pages[page+1]; ok {
		next = page + 1
	}
	return &github.SelectedReposList{Repositories: f.pages[page]}, &github.Response{NextPage: next}, nil
}

func repo(name string, id int64) *github.Repository {
	return &github.Repository{Name: github.Ptr(name), ID: github.Ptr(id)}
}

func TestListRepoNames(t *testing.T) {
	boom := errors.New("boom")
	cases := map[string]struct {
		lister    *fakeLister
		want      []string
		wantErr   error
		wantCalls int
	}{
		"Empty":      {lister: &fakeLister{pages: map[int][]*github.Repository{1: nil}}, want: []string{}, wantCalls: 1},
		"SinglePage": {lister: &fakeLister{pages: map[int][]*github.Repository{1: {repo("A", 1), repo("b", 2)}}}, want: []string{"A", "b"}, wantCalls: 1},
		"Paginated": {
			lister:    &fakeLister{pages: map[int][]*github.Repository{1: {repo("a", 1)}, 2: {repo("b", 2)}, 3: {repo("c", 3)}}},
			want:      []string{"a", "b", "c"},
			wantCalls: 3,
		},
		"ErrorFirstPage": {lister: &fakeLister{err: boom, errPage: 0}, wantErr: boom, wantCalls: 1},
		"ErrorLaterPage": {
			lister:  &fakeLister{pages: map[int][]*github.Repository{1: {repo("a", 1)}, 2: {repo("b", 2)}}, err: boom, errPage: 2},
			wantErr: boom, wantCalls: 2,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := ListRepoNames(context.Background(), tc.lister, "org", "SECRET")
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ListRepoNames(...): want error %v, got %v", tc.wantErr, err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("ListRepoNames(...): -want, +got:\n%s", diff)
			}
			if len(tc.lister.calls) != tc.wantCalls {
				t.Errorf("ListRepoNames(...): want %d calls, got %d", tc.wantCalls, len(tc.lister.calls))
			}
			for _, c := range tc.lister.calls {
				if c.PerPage != 100 {
					t.Errorf("ListRepoNames(...): want PerPage 100, got %d", c.PerPage)
				}
			}
		})
	}
}

func TestListRepoIDs(t *testing.T) {
	boom := errors.New("boom")
	cases := map[string]struct {
		lister  *fakeLister
		want    map[string]int64
		wantErr error
	}{
		"Empty": {lister: &fakeLister{pages: map[int][]*github.Repository{1: nil}}, want: map[string]int64{}},
		"LowercasesNames": {
			lister: &fakeLister{pages: map[int][]*github.Repository{1: {repo("Repo-A", 11), repo("repo-b", 22)}}},
			want:   map[string]int64{"repo-a": 11, "repo-b": 22},
		},
		"Paginated": {
			lister: &fakeLister{pages: map[int][]*github.Repository{1: {repo("A", 1)}, 2: {repo("B", 2)}}},
			want:   map[string]int64{"a": 1, "b": 2},
		},
		"Error": {lister: &fakeLister{err: boom}, wantErr: boom},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := ListRepoIDs(context.Background(), tc.lister, "org", "SECRET")
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ListRepoIDs(...): want error %v, got %v", tc.wantErr, err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("ListRepoIDs(...): -want, +got:\n%s", diff)
			}
		})
	}
}

func TestVisibilitySelected(t *testing.T) {
	if VisibilitySelected != "selected" {
		t.Errorf("VisibilitySelected: want %q, got %q", "selected", VisibilitySelected)
	}
}

func TestConditionMessagesNameTheSecret(t *testing.T) {
	if m := SecretNotFound("X_SECRET").Message; !strings.Contains(m, "X_SECRET") {
		t.Errorf("SecretNotFound message %q does not name the secret", m)
	}
}

var _ RepoLister = (*fakeLister)(nil)
