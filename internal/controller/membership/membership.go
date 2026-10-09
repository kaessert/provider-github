/*
Copyright 2022 The Crossplane Authors.

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

package membership

import (
	"context"
	"time"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/google/go-github/v90/github"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/pkg/connection"
	"github.com/crossplane/crossplane-runtime/pkg/controller"
	"github.com/crossplane/crossplane-runtime/pkg/event"
	"github.com/crossplane/crossplane-runtime/pkg/meta"
	"github.com/crossplane/crossplane-runtime/pkg/ratelimiter"
	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/pkg/resource"

	"github.com/crossplane/provider-github/apis/organizations/v1alpha1"
	apisv1alpha1 "github.com/crossplane/provider-github/apis/v1alpha1"
	ghclient "github.com/crossplane/provider-github/internal/clients"
	"github.com/crossplane/provider-github/internal/features"
	"github.com/crossplane/provider-github/internal/telemetry"
)

const (
	errNotMembership = "managed resource is not a Membership custom resource"
	errTrackPCUsage  = "cannot track ProviderConfig usage"
	errGetPC         = "cannot get ProviderConfig"

	errNewClient = "cannot create new Service"
)

// Setup adds a controller that reconciles Membership managed resources.
// roleDirectMember is the GitHub API name of the plain organization member role.
const roleDirectMember = "direct_member"

func Setup(mgr ctrl.Manager, o controller.Options, metrics *telemetry.RateLimitMetrics) error {
	return SetupWithTimeout(mgr, o, metrics, 0) // Use default timeout
}

// SetupWithTimeout adds a controller that reconciles Membership managed resources with configurable timeout.
func SetupWithTimeout(mgr ctrl.Manager, o controller.Options, metrics *telemetry.RateLimitMetrics, timeout time.Duration) error {
	name := managed.ControllerName(v1alpha1.MembershipGroupKind)

	cps := []managed.ConnectionPublisher{managed.NewAPISecretPublisher(mgr.GetClient(), mgr.GetScheme())}
	if o.Features.Enabled(features.EnableAlphaExternalSecretStores) {
		cps = append(cps, connection.NewDetailsManager(mgr.GetClient(), apisv1alpha1.StoreConfigGroupVersionKind))
	}

	reconcilerOptions := []managed.ReconcilerOption{
		managed.WithExternalConnecter(&connector{
			kube:    mgr.GetClient(),
			usage:   resource.NewProviderConfigUsageTracker(mgr.GetClient(), &apisv1alpha1.ProviderConfigUsage{}),
			metrics: metrics}),
		managed.WithLogger(o.Logger.WithValues("controller", name)),
		managed.WithPollInterval(o.PollInterval),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))),
		managed.WithConnectionPublishers(cps...),
	}

	// Add timeout if specified
	if timeout > 0 {
		reconcilerOptions = append(reconcilerOptions, managed.WithTimeout(timeout))
	}

	r := managed.NewReconciler(mgr,
		resource.ManagedKind(v1alpha1.MembershipGroupVersionKind),
		reconcilerOptions...)

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		WithOptions(o.ForControllerRuntime()).
		WithEventFilter(resource.DesiredStateChanged()).
		For(&v1alpha1.Membership{}).
		Complete(ratelimiter.NewReconciler(name, r, o.GlobalRateLimiter))
}

type connector struct {
	kube    client.Client
	usage   resource.Tracker
	metrics *telemetry.RateLimitMetrics
}

func (c *connector) Connect(ctx context.Context, mg resource.Managed) (managed.ExternalClient, error) {
	cr, ok := mg.(*v1alpha1.Membership)
	if !ok {
		return nil, errors.New(errNotMembership)
	}

	if err := c.usage.Track(ctx, mg); err != nil {
		return nil, errors.Wrap(err, errTrackPCUsage)
	}

	pc := &apisv1alpha1.ProviderConfig{}
	if err := c.kube.Get(ctx, types.NamespacedName{Name: cr.GetProviderConfigReference().Name}, pc); err != nil {
		return nil, errors.Wrap(err, errGetPC)
	}

	gh, err := ghclient.ResolveAndConnect(ctx, c.kube, pc, c.metrics, cr.Spec.ForProvider.Org)
	if err != nil {
		return nil, errors.Wrap(err, errNewClient)
	}

	return &external{github: gh}, nil
}

type external struct {
	github *ghclient.Client
}

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr, ok := mg.(*v1alpha1.Membership)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotMembership)
	}

	name := meta.GetExternalName(cr)
	m, _, err := c.github.Organizations.GetOrgMembership(ctx, name, cr.Spec.ForProvider.Org)

	if ghclient.Is404(err) {
		return managed.ExternalObservation{ResourceExists: false}, nil
	}
	if err != nil {
		return managed.ExternalObservation{}, err
	}

	if *m.Role != cr.Spec.ForProvider.Role {
		return managed.ExternalObservation{
			ResourceExists:   true,
			ResourceUpToDate: false,
		}, nil
	}

	cr.SetConditions(xpv1.Available())

	return managed.ExternalObservation{
		ResourceExists:   true,
		ResourceUpToDate: true,
	}, nil
}

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	cr, ok := mg.(*v1alpha1.Membership)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotMembership)
	}

	name := meta.GetExternalName(cr)

	user, _, err := c.github.Users.Get(ctx, name)
	if err != nil {
		return managed.ExternalCreation{}, err
	}

	role := cr.Spec.ForProvider.Role
	if role == roleDirectMember {
		return managed.ExternalCreation{}, errors.New("Don't use `direct_member`, use `member` instead!")
	}
	if role == "member" {
		role = roleDirectMember
	}

	inv := &github.CreateOrgInvitationOptions{
		InviteeID: user.ID,
		Role:      &role,
		TeamID:    []int64{},
	}

	_, _, err = c.github.Organizations.CreateOrgInvitation(ctx, cr.Spec.ForProvider.Org, inv)
	if err != nil {
		return managed.ExternalCreation{}, err
	}

	cr.SetConditions(xpv1.Available())

	return managed.ExternalCreation{}, nil
}

func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	cr, ok := mg.(*v1alpha1.Membership)
	if !ok {
		return managed.ExternalUpdate{}, errors.New(errNotMembership)
	}

	name := meta.GetExternalName(cr)
	u, _, err := c.github.Users.Get(ctx, name)
	if err != nil {
		return managed.ExternalUpdate{}, err
	}

	m := &github.Membership{
		User: u,
		Role: &cr.Spec.ForProvider.Role,
	}

	_, _, err = c.github.Organizations.EditOrgMembership(ctx, name, cr.Spec.ForProvider.Org, m)
	if err != nil {
		return managed.ExternalUpdate{}, err
	}

	return managed.ExternalUpdate{}, nil
}

func (c *external) Delete(ctx context.Context, mg resource.Managed) error {
	cr, ok := mg.(*v1alpha1.Membership)
	if !ok {
		return errors.New(errNotMembership)
	}

	name := meta.GetExternalName(cr)
	role := cr.Spec.ForProvider.Role

	if role == "admin" {
		return errors.New("You can only delete member users. Admin users cannot be deleted")
	}

	_, err := c.github.Organizations.RemoveOrgMembership(ctx, name, cr.Spec.ForProvider.Org)
	if err != nil {
		return err
	}

	return nil
}
