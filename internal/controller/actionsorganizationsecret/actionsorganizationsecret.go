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

package actionsorganizationsecret

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/google/go-github/v90/github"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	"github.com/provider-github/provider-github/apis/cluster/organizations/v1alpha1"
	ghclient "github.com/provider-github/provider-github/internal/clients"
	"github.com/provider-github/provider-github/internal/controller/mgmtpolicy"
	"github.com/provider-github/provider-github/internal/controller/secretaccess"
)

const (
	errNotActionsOrganizationSecret = "managed resource is not an ActionsOrganizationSecret custom resource"
	errTrackPCUsage                 = "cannot track ProviderConfig usage"
	errGetPC                        = "cannot get ProviderConfig"
	errNewClient                    = "cannot create new Service"
	errGetSecret                    = "cannot get organization secret"
	errListRepos                    = "cannot list the repositories that can use the organization secret"
	errResolveRepo                  = "cannot resolve selected repository"
	errNoValueSecretRef             = "spec.forProvider.valueSecretRef must be set to write the organization secret"
	errNoVisibility                 = "spec.forProvider.visibility must be set to write the organization secret"
	errGetValueSecret               = "cannot get the Kubernetes Secret that valueSecretRef references"
	errNoValueKey                   = "the Kubernetes Secret that valueSecretRef references has no such key"
	errPutSecret                    = "cannot create or update organization secret"
	errDeleteSecret                 = "cannot delete organization secret"
	errReadBack                     = "cannot read back organization secret after writing it"
)

// external reconciles one ActionsOrganizationSecret. The value of the secret is
// write-only: GitHub never returns it, so it is sealed on every write and a
// change to it is detected from the records in status.lastWrite.
type external struct {
	github *ghclient.Client
	kube   client.Client

	// secretNamespace, when set, is the only namespace the value Secret is read
	// from, whatever namespace the spec names. Namespaced resources set it to
	// their own namespace.
	secretNamespace string
}

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr, ok := mg.(*v1alpha1.ActionsOrganizationSecret)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotActionsOrganizationSecret)
	}

	name := meta.GetExternalName(cr)
	org := cr.Spec.ForProvider.Org

	s, _, err := c.github.Actions.GetOrgSecret(ctx, org, name)
	if ghclient.Is404(err) {
		return managed.ExternalObservation{ResourceExists: false}, nil
	}
	if err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, errGetSecret)
	}

	cr.Status.AtProvider.ID = name
	cr.Status.AtProvider.Org = org
	cr.Status.AtProvider.Name = s.GetName()
	cr.Status.AtProvider.Visibility = s.Visibility
	cr.Status.AtProvider.CreatedAt = formatTime(s.CreatedAt)
	cr.Status.AtProvider.UpdatedAt = formatTime(s.UpdatedAt)
	cr.Status.AtProvider.SelectedRepositories = nil

	// The list exists on GitHub's side while GitHub's visibility is selected,
	// whatever the spec says; it is read and mirrored before any difference is
	// reported.
	var ghRepos []string
	if s.Visibility == secretaccess.VisibilitySelected {
		ghRepos, err = secretaccess.ListRepoNames(ctx, c.github.Actions, org, name)
		if err != nil {
			return managed.ExternalObservation{}, errors.Wrap(err, errListRepos)
		}
		cr.Status.AtProvider.SelectedRepositories = secretaccess.RepoObservations(ghRepos)
	}

	drift := configDrifted(cr.Spec.ForProvider, s.Visibility, ghRepos)

	// A resource being deleted needs no value: the Kubernetes Secret may be gone
	// already, and nothing is written. Under a policy that does not write there
	// is no value to keep current either.
	if !drift && !meta.WasDeleted(cr) && mgmtpolicy.WritesDeclared(cr.GetManagementPolicies()) {
		drift, err = c.valueDrifted(ctx, cr, s)
		if err != nil {
			return managed.ExternalObservation{}, err
		}
	}

	if drift {
		cr.SetConditions(xpv2.Unavailable())
		return managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: false}, nil
	}

	cr.SetConditions(xpv2.Available())
	return managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: true}, nil
}

// configDrifted compares what the spec declares with what GitHub reports,
// leaving out the value. An omitted visibility, and an omitted
// selectedRepositories, are not compared: the latter means another resource may
// own the list. The org is identity and is never compared. Repository names
// compare case-insensitively.
func configDrifted(p v1alpha1.ActionsOrganizationSecretParameters, ghVisibility string, ghRepos []string) bool {
	if p.Visibility != "" && p.Visibility != ghVisibility {
		return true
	}
	if p.Visibility == secretaccess.VisibilitySelected && ghVisibility == secretaccess.VisibilitySelected && p.SelectedRepositories != nil {
		return !secretaccess.SameRepos(p.SelectedRepositories, ghRepos)
	}
	return false
}

// valueDrifted reports whether the secret value may differ from what GitHub
// holds. GitHub never returns the value, so two records of the last successful
// write stand in for it: the identity of the referenced Kubernetes Secret, which
// changes when the desired value does, and GitHub's updated_at, which advances
// when anyone else writes the secret. A missing record counts as drift once; the
// write that follows re-establishes it.
func (c *external) valueDrifted(ctx context.Context, cr *v1alpha1.ActionsOrganizationSecret, s *github.Secret) (bool, error) {
	ref := cr.Spec.ForProvider.ValueSecretRef
	if ref == nil {
		// Nothing can be written without a value; the write reports it.
		return true, nil
	}
	lw := cr.Status.LastWrite
	if lw == nil || lw.SecretUID == "" || lw.SecretResourceVersion == "" || lw.UpdatedAt == "" {
		return true, nil
	}

	ks, err := c.valueSecret(ctx, ref)
	if err != nil {
		return false, err
	}
	if string(ks.UID) != lw.SecretUID || ks.ResourceVersion != lw.SecretResourceVersion {
		return true, nil
	}

	recorded, err := time.Parse(time.RFC3339, lw.UpdatedAt)
	if err != nil {
		return true, nil //nolint:nilerr // an unreadable record is a missing record: the write re-establishes it.
	}
	return s.UpdatedAt.After(recorded), nil
}

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	cr, ok := mg.(*v1alpha1.ActionsOrganizationSecret)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotActionsOrganizationSecret)
	}
	return managed.ExternalCreation{}, c.write(ctx, cr)
}

func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	cr, ok := mg.(*v1alpha1.ActionsOrganizationSecret)
	if !ok {
		return managed.ExternalUpdate{}, errors.New(errNotActionsOrganizationSecret)
	}
	return managed.ExternalUpdate{}, c.write(ctx, cr)
}

// write creates or updates the secret with one PUT, which replaces the secret
// whole: it always carries the sealed value and the visibility, and the
// selected repository ids while the visibility is selected. It then reads the
// secret back for the updated_at that marks the provider's own write.
func (c *external) write(ctx context.Context, cr *v1alpha1.ActionsOrganizationSecret) error {
	p := cr.Spec.ForProvider
	if p.ValueSecretRef == nil {
		return errors.New(errNoValueSecretRef)
	}
	if p.Visibility == "" {
		return errors.New(errNoVisibility)
	}
	name := meta.GetExternalName(cr)

	ks, err := c.valueSecret(ctx, p.ValueSecretRef)
	if err != nil {
		return err
	}
	value, ok := ks.Data[p.ValueSecretRef.Key]
	if !ok {
		return errors.Errorf("%s: key %q in Secret %s/%s", errNoValueKey, p.ValueSecretRef.Key, ks.Namespace, ks.Name)
	}

	ids, err := c.selectedIDs(ctx, cr)
	if err != nil {
		return err
	}

	req := github.SecretOrgRequest{Visibility: p.Visibility, SelectedRepositoryIDs: ids}
	if err := c.put(ctx, p.Org, name, value, req); err != nil {
		return err
	}

	// The records are rebuilt from this write alone: a failed read-back leaves
	// none, so the next Observe reports the secret out of date and writes again.
	cr.Status.LastWrite = nil
	s, _, err := c.github.Actions.GetOrgSecret(ctx, p.Org, name)
	if err != nil {
		return errors.Wrap(err, errReadBack)
	}
	cr.Status.LastWrite = &v1alpha1.ActionsOrganizationSecretWriteRecord{
		SecretUID:             string(ks.UID),
		SecretResourceVersion: ks.ResourceVersion,
		UpdatedAt:             formatTime(s.UpdatedAt),
	}
	return nil
}

// selectedIDs returns the repository ids to send with the PUT, nil unless the
// visibility is selected. A PUT replaces the list, so an omitted
// selectedRepositories resends the ids GitHub already holds, which keeps a list
// owned by another resource intact. A declared list is resolved to ids here, and
// a repository that does not exist fails the write: GitHub would drop an unknown
// id silently.
func (c *external) selectedIDs(ctx context.Context, cr *v1alpha1.ActionsOrganizationSecret) ([]int64, error) {
	p := cr.Spec.ForProvider
	if p.Visibility != secretaccess.VisibilitySelected {
		return nil, nil
	}
	name := meta.GetExternalName(cr)

	// A secret that does not exist yet has no list.
	known, err := secretaccess.ListRepoIDs(ctx, c.github.Actions, p.Org, name)
	if ghclient.Is404(err) {
		known, err = map[string]int64{}, nil
	}
	if err != nil {
		return nil, errors.Wrap(err, errListRepos)
	}

	if p.SelectedRepositories == nil {
		ids := make([]int64, 0, len(known))
		for _, id := range known {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		return ids, nil
	}

	names := secretaccess.RepoNames(p.SelectedRepositories)
	var added []string
	for _, n := range names {
		if _, ok := known[strings.ToLower(n)]; !ok {
			added = append(added, n)
		}
	}
	resolver := ghclient.NewRepoIDResolver(c.github, p.Org)
	addedIDs := make([]int64, 0, len(added))
	for _, n := range added {
		id, err := resolver.BatchGetIDs(ctx, []string{n})
		if err != nil {
			return nil, errors.Wrapf(err, "%s %q", errResolveRepo, n)
		}
		addedIDs = append(addedIDs, id...)
	}
	if err := secretaccess.RenamedRepoError(known, added, addedIDs); err != nil {
		return nil, err
	}
	for i, n := range added {
		known[strings.ToLower(n)] = addedIDs[i]
	}
	ids := make([]int64, 0, len(names))
	for _, n := range names {
		ids = append(ids, known[strings.ToLower(n)])
	}
	return ids, nil
}

func (c *external) Delete(ctx context.Context, mg resource.Managed) (managed.ExternalDelete, error) {
	cr, ok := mg.(*v1alpha1.ActionsOrganizationSecret)
	if !ok {
		return managed.ExternalDelete{}, errors.New(errNotActionsOrganizationSecret)
	}

	_, err := c.github.Actions.DeleteOrgSecret(ctx, cr.Spec.ForProvider.Org, meta.GetExternalName(cr))
	if err != nil && !ghclient.Is404(err) {
		return managed.ExternalDelete{}, errors.Wrap(err, errDeleteSecret)
	}
	return managed.ExternalDelete{}, nil
}

// valueSecret reads the Kubernetes Secret that holds the value.
func (c *external) valueSecret(ctx context.Context, ref *xpv2.SecretKeySelector) (*corev1.Secret, error) {
	ns := ref.Namespace
	if c.secretNamespace != "" {
		ns = c.secretNamespace
	}
	ks := &corev1.Secret{}
	if err := c.kube.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: ns}, ks); err != nil {
		return nil, errors.Wrapf(err, "%s: %s/%s", errGetValueSecret, ns, ref.Name)
	}
	return ks, nil
}

// formatTime renders a GitHub timestamp in RFC 3339, empty when GitHub sent none.
func formatTime(t github.Timestamp) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// Disconnect is a no-op: the GitHub client is cached and shared across
// reconciles, so there is nothing to release per connection.
func (c *external) Disconnect(_ context.Context) error {
	return nil
}
