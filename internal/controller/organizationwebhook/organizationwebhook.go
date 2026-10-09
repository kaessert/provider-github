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

package organizationwebhook

import (
	"context"
	"slices"
	"strconv"

	"github.com/google/go-github/v90/github"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	pointer "k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	"github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	ghclient "github.com/crossplane/provider-github/internal/clients"
	"github.com/crossplane/provider-github/internal/util"
)

const (
	errNotOrganizationWebhook = "managed resource is not an OrganizationWebhook custom resource"
	errTrackPCUsage           = "cannot track ProviderConfig usage"
	errGetPC                  = "cannot get ProviderConfig"
	errNewClient              = "cannot create new Service"
	errNoID                   = "webhook ID is not known"
	errNoConnectionSecretRef  = "spec.writeConnectionSecretToRef must be set when secretKeyRef is used"

	// connectionSecretKey holds the last applied webhook secret in the connection secret.
	connectionSecretKey = "secret"
)

type external struct {
	github *ghclient.Client
	kube   client.Client

	// secretNamespace, when set, is the only namespace the webhook secret is
	// read from, whatever namespace the spec names. Namespaced resources set it
	// to their own namespace.
	secretNamespace string
}

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr, ok := mg.(*v1alpha1.OrganizationWebhook)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotOrganizationWebhook)
	}

	p := cr.Spec.ForProvider
	if p.SecretKeyRef != nil && cr.Spec.WriteConnectionSecretToReference == nil && !meta.WasDeleted(cr) {
		return managed.ExternalObservation{}, errors.New(errNoConnectionSecretRef)
	}

	h, err := c.findHook(ctx, cr)
	if err != nil {
		return managed.ExternalObservation{}, err
	}
	if h == nil {
		return managed.ExternalObservation{ResourceExists: false}, nil
	}
	cr.Status.AtProvider.ID = h.GetID()
	mirrorHook(&cr.Status.AtProvider, h, p.Org)

	// Persist the adopted hook ID as the external name.
	id := strconv.FormatInt(h.GetID(), 10)
	lateInit := meta.GetExternalName(cr) != id
	if lateInit {
		meta.SetExternalName(cr, id)
	}

	if !configUpToDate(h, p) {
		cr.SetConditions(xpv2.Unavailable())
		return managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: false, ResourceLateInitialized: lateInit}, nil
	}
	secretOK, err := c.secretUpToDate(ctx, cr, h)
	if err != nil {
		return managed.ExternalObservation{}, err
	}
	if !secretOK {
		cr.SetConditions(xpv2.Unavailable())
		return managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: false, ResourceLateInitialized: lateInit}, nil
	}

	cr.SetConditions(xpv2.Available())
	return managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: true, ResourceLateInitialized: lateInit}, nil
}

// mirrorHook records in status.atProvider what GitHub reports for the hook h,
// which was read under org. The webhook secret is never returned by GitHub.
func mirrorHook(ap *v1alpha1.OrganizationWebhookObservation, h *github.Hook, org string) {
	ap.Org = org
	ap.URL = h.Config.GetURL()
	ap.ContentType = h.Config.GetContentType()
	ap.Events = slices.Sorted(slices.Values(h.Events))
	ap.Active = nil
	if h.Active != nil {
		active := h.GetActive()
		ap.Active = &active
	}
	insecure := h.Config.GetInsecureSSL() == "1"
	ap.InsecureSSL = &insecure
}

// configUpToDate compares every hook field except the secret. A url,
// contentType or events the spec omits (an Observe-only import) is not compared.
func configUpToDate(h *github.Hook, p v1alpha1.OrganizationWebhookParameters) bool {
	return (p.URL == "" || h.Config.GetURL() == p.URL) &&
		(p.ContentType == "" || h.Config.GetContentType() == p.ContentType) &&
		(len(p.Events) == 0 || util.EqualUnordered(h.Events, p.Events)) &&
		h.GetActive() == pointer.Deref(p.Active, true) &&
		(h.Config.GetInsecureSSL() == "1") == pointer.Deref(p.InsecureSSL, false)
}

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	cr, ok := mg.(*v1alpha1.OrganizationWebhook)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotOrganizationWebhook)
	}

	hook, secret, err := c.desiredHook(ctx, cr)
	if err != nil {
		return managed.ExternalCreation{}, err
	}

	h, _, err := c.github.Organizations.CreateHook(ctx, cr.Spec.ForProvider.Org, hook)
	if err != nil {
		return managed.ExternalCreation{}, err
	}
	meta.SetExternalName(cr, strconv.FormatInt(h.GetID(), 10))
	cr.Status.AtProvider.ID = h.GetID()

	return managed.ExternalCreation{ConnectionDetails: connectionDetails(secret)}, nil
}

func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	cr, ok := mg.(*v1alpha1.OrganizationWebhook)
	if !ok {
		return managed.ExternalUpdate{}, errors.New(errNotOrganizationWebhook)
	}

	id, ok := hookID(cr)
	if !ok {
		return managed.ExternalUpdate{}, errors.New(errNoID)
	}

	hook, secret, err := c.desiredHook(ctx, cr)
	if err != nil {
		return managed.ExternalUpdate{}, err
	}

	if _, _, err := c.github.Organizations.EditHook(ctx, cr.Spec.ForProvider.Org, id, hook); err != nil {
		return managed.ExternalUpdate{}, err
	}

	return managed.ExternalUpdate{ConnectionDetails: connectionDetails(secret)}, nil
}

func (c *external) Delete(ctx context.Context, mg resource.Managed) (managed.ExternalDelete, error) {
	cr, ok := mg.(*v1alpha1.OrganizationWebhook)
	if !ok {
		return managed.ExternalDelete{}, errors.New(errNotOrganizationWebhook)
	}

	id, ok := hookID(cr)
	if !ok {
		return managed.ExternalDelete{}, nil
	}

	_, err := c.github.Organizations.DeleteHook(ctx, cr.Spec.ForProvider.Org, id)
	if err != nil && !ghclient.Is404(err) {
		return managed.ExternalDelete{}, err
	}
	return managed.ExternalDelete{}, nil
}

// findHook gets the hook by the ID in the external name, or else
// looks it up by URL. It returns nil if there is none.
func (c *external) findHook(ctx context.Context, cr *v1alpha1.OrganizationWebhook) (*github.Hook, error) {
	org := cr.Spec.ForProvider.Org
	if id, ok := hookID(cr); ok {
		h, _, err := c.github.Organizations.GetHook(ctx, org, id)
		if ghclient.Is404(err) {
			return nil, nil
		}
		return h, err
	}

	// Without a url there is nothing to match a hook by.
	if cr.Spec.ForProvider.URL == "" {
		return nil, nil
	}

	opts := &github.ListOptions{PerPage: 100}
	for {
		hooks, resp, err := c.github.Organizations.ListHooks(ctx, org, opts)
		if err != nil {
			return nil, err
		}
		for _, h := range hooks {
			if h.Config.GetURL() == cr.Spec.ForProvider.URL {
				return h, nil
			}
		}
		if resp.NextPage == 0 {
			return nil, nil
		}
		opts.Page = resp.NextPage
	}
}

// secretUpToDate compares the referenced secret with the one last
// applied, since GitHub only reports whether a secret is set.
func (c *external) secretUpToDate(ctx context.Context, cr *v1alpha1.OrganizationWebhook, h *github.Hook) (bool, error) {
	ghHasSecret := h.Config != nil && h.Config.Secret != nil
	if cr.Spec.ForProvider.SecretKeyRef == nil {
		return !ghHasSecret, nil
	}
	if !ghHasSecret {
		return false, nil
	}

	desired, err := c.desiredSecret(ctx, cr.Spec.ForProvider.SecretKeyRef)
	if err != nil {
		return false, err
	}
	applied, err := c.appliedSecret(ctx, cr.Spec.WriteConnectionSecretToReference)
	if err != nil {
		return false, err
	}
	return applied == desired, nil
}

// desiredHook builds the GitHub hook from the spec and returns the
// resolved secret, empty when the spec has none.
func (c *external) desiredHook(ctx context.Context, cr *v1alpha1.OrganizationWebhook) (*github.Hook, string, error) {
	p := cr.Spec.ForProvider
	insecureSsl := "0"
	if pointer.Deref(p.InsecureSSL, false) {
		insecureSsl = "1"
	}
	hook := &github.Hook{
		Config: &github.HookConfig{
			ContentType: github.Ptr(p.ContentType),
			InsecureSSL: github.Ptr(insecureSsl),
			URL:         github.Ptr(p.URL),
		},
		Events: p.Events,
		Active: github.Ptr(pointer.Deref(p.Active, true)),
	}
	if p.SecretKeyRef == nil {
		return hook, "", nil
	}
	secret, err := c.desiredSecret(ctx, p.SecretKeyRef)
	if err != nil {
		return nil, "", err
	}
	hook.Config.Secret = github.Ptr(secret)
	return hook, secret, nil
}

func (c *external) desiredSecret(ctx context.Context, ref *xpv2.SecretKeySelector) (string, error) {
	ns := ref.Namespace
	if c.secretNamespace != "" {
		ns = c.secretNamespace
	}
	s := &corev1.Secret{}
	if err := c.kube.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: ns}, s); err != nil {
		return "", errors.Wrapf(err, "cannot get secret `%s/%s`", ns, ref.Name)
	}
	data, ok := s.Data[ref.Key]
	if !ok {
		return "", errors.Errorf("secret key `%s` not found in secret `%s/%s`", ref.Key, ns, ref.Name)
	}
	return string(data), nil
}

// appliedSecret reads the last applied secret from the connection
// secret, which may not exist yet.
func (c *external) appliedSecret(ctx context.Context, ref *xpv2.SecretReference) (string, error) {
	s := &corev1.Secret{}
	if err := c.kube.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: ref.Namespace}, s); resource.IgnoreNotFound(err) != nil {
		return "", errors.Wrapf(err, "cannot get connection secret `%s/%s`", ref.Namespace, ref.Name)
	}
	return string(s.Data[connectionSecretKey]), nil
}

func connectionDetails(secret string) managed.ConnectionDetails {
	if secret == "" {
		return nil
	}
	return managed.ConnectionDetails{connectionSecretKey: []byte(secret)}
}

// hookID parses the hook ID from the external name, which holds the CR
// name until the hook is created or adopted.
func hookID(cr *v1alpha1.OrganizationWebhook) (int64, bool) {
	id, err := strconv.ParseInt(meta.GetExternalName(cr), 10, 64)
	return id, err == nil
}

// Disconnect is a no-op: the GitHub client is cached and shared across
// reconciles, so there is nothing to release per connection.
func (c *external) Disconnect(_ context.Context) error {
	return nil
}
