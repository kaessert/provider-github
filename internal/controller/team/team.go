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

package team

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	pointer "k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/google/go-github/v90/github"
	"github.com/gosimple/slug"

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

// GitHub team membership roles.
const (
	roleMember     = "member"
	roleMaintainer = "maintainer"
)

const (
	errNotTeam      = "managed resource is not a Team custom resource"
	errTrackPCUsage = "cannot track ProviderConfig usage"
	errGetPC        = "cannot get ProviderConfig"

	errNewClient = "cannot create new Service"
)

// Setup adds a controller that reconciles Team managed resources.
func Setup(mgr ctrl.Manager, o controller.Options, metrics *telemetry.RateLimitMetrics) error {
	return SetupWithTimeout(mgr, o, metrics, 0) // Use default timeout
}

// SetupWithTimeout adds a controller that reconciles Team managed resources with configurable timeout.
func SetupWithTimeout(mgr ctrl.Manager, o controller.Options, metrics *telemetry.RateLimitMetrics, timeout time.Duration) error {
	name := managed.ControllerName(v1alpha1.TeamGroupKind)

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
		resource.ManagedKind(v1alpha1.TeamGroupVersionKind),
		reconcilerOptions...)

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		WithOptions(o.ForControllerRuntime()).
		WithEventFilter(resource.DesiredStateChanged()).
		For(&v1alpha1.Team{}).
		Complete(ratelimiter.NewReconciler(name, r, o.GlobalRateLimiter))
}

type connector struct {
	kube    client.Client
	usage   resource.Tracker
	metrics *telemetry.RateLimitMetrics
}

func (c *connector) Connect(ctx context.Context, mg resource.Managed) (managed.ExternalClient, error) {
	cr, ok := mg.(*v1alpha1.Team)
	if !ok {
		return nil, errors.New(errNotTeam)
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

const (
	typeTeamMembershipPartial   xpv1.ConditionType   = "TeamMembershipPartial"
	reasonPendingOrgMembership  xpv1.ConditionReason = "PendingOrgMembership"
	reasonRoleEnforcedByOrg     xpv1.ConditionReason = "RoleEnforcedByOrg"
	reasonPendingTeamInvitation xpv1.ConditionReason = "PendingTeamInvitation"
	reasonAllMembersPresent     xpv1.ConditionReason = "AllMembersPresent"
)

// setTeamMembershipPartialCondition writes the CR-side condition reporting members the controller skipped.
func setTeamMembershipPartialCondition(ctx context.Context, cr *v1alpha1.Team, pendingOrg, pendingTeam, roleEnforced []string) {
	c := xpv1.Condition{
		Type:               typeTeamMembershipPartial,
		LastTransitionTime: metav1.Now(),
	}
	if len(pendingOrg) == 0 && len(pendingTeam) == 0 && len(roleEnforced) == 0 {
		c.Status = corev1.ConditionFalse
		c.Reason = reasonAllMembersPresent
		cr.SetConditions(c)
		return
	}

	c.Status = corev1.ConditionTrue
	// Reason priority: most-actionable first.
	switch {
	case len(pendingOrg) > 0:
		c.Reason = reasonPendingOrgMembership
	case len(roleEnforced) > 0:
		c.Reason = reasonRoleEnforcedByOrg
	default:
		c.Reason = reasonPendingTeamInvitation
	}

	var parts []string
	if len(pendingOrg) > 0 {
		sort.Strings(pendingOrg)
		parts = append(parts, fmt.Sprintf("declared but not yet org members: %s", strings.Join(pendingOrg, ", ")))
	}
	if len(roleEnforced) > 0 {
		sort.Strings(roleEnforced)
		parts = append(parts, fmt.Sprintf("declared role overridden by GitHub org-admin enforcement: %s", strings.Join(roleEnforced, ", ")))
	}
	if len(pendingTeam) > 0 {
		sort.Strings(pendingTeam)
		parts = append(parts, fmt.Sprintf("residual pending team invitations: %s", strings.Join(pendingTeam, ", ")))
	}
	c.Message = strings.Join(parts, "; ")

	ctrl.LoggerFrom(ctx).Info("team membership partial",
		"team", meta.GetExternalName(cr),
		"pendingOrg", pendingOrg,
		"pendingTeam", pendingTeam,
		"roleEnforced", roleEnforced)

	cr.SetConditions(c)
}

// memberCategorization buckets CR and direct team members for Observe and Update.
type memberCategorization struct {
	// toRemove: users currently direct on the team but not in the CR.
	toRemove map[string]string
	// roleUpdate: users on the team in the CR but with a different role.
	roleUpdate map[string]string
	// inviteable: CR users who are active org members but not yet on the team.
	inviteable map[string]string
	// pendingOrg: CR users who are not active org members; skipped to avoid org-invite side effect.
	pendingOrg []string
	// pendingTeam: CR users with a residual unaccepted team invitation; treated as in flight.
	pendingTeam []string
	// roleEnforced: members whose role GitHub force-applies (org admins → maintainer).
	roleEnforced []string
}

func (c *memberCategorization) hasMemberDrift() bool {
	return len(c.toRemove) > 0 || len(c.roleUpdate) > 0 || len(c.inviteable) > 0
}

// categorizeMembers assigns the union of CR and direct team members to memberCategorization buckets.
func categorizeMembers(ctx context.Context, gh *ghclient.Client, org, slug string, members []v1alpha1.TeamMemberUser) (*memberCategorization, error) {
	crMToPermission := getUserPermissionMapFromCr(members)
	rollup, err := getMembersWithPermissions(ctx, gh, org, slug)
	if err != nil {
		return nil, err
	}
	// Only members not yet on the team can have a pending invite; skip the fetch when there are none.
	pendingSet := map[string]bool{}
	for user := range crMToPermission {
		if _, ok := rollup[user]; ok {
			continue
		}
		pendingLogins, err := getPendingTeamInviteeLogins(ctx, gh, org, slug)
		if err != nil {
			return nil, err
		}
		for _, l := range pendingLogins {
			pendingSet[l] = true
		}
		break
	}
	inheritedSet, err := collectChildMemberLogins(ctx, gh, org, slug)
	if err != nil {
		return nil, err
	}

	out := &memberCategorization{
		toRemove:   make(map[string]string),
		roleUpdate: make(map[string]string),
		inviteable: make(map[string]string),
	}

	for user, ghRole := range rollup {
		crRole, inCR := crMToPermission[user]
		verdict, err := classifyRollupUser(ctx, gh, org, user, ghRole, crRole, inCR, inheritedSet[user])
		if err != nil {
			return nil, err
		}
		switch verdict {
		case verdictRemove:
			out.toRemove[user] = ghRole
		case verdictUpdateRole:
			out.roleUpdate[user] = crRole
		case verdictRoleEnforced:
			out.roleEnforced = append(out.roleEnforced, user)
		case verdictNone:
			// In sync, or an inherited member the controller must not touch.
		}
	}

	for user, crRole := range crMToPermission {
		if _, ok := rollup[user]; ok {
			continue
		}
		if pendingSet[user] {
			out.pendingTeam = append(out.pendingTeam, user)
			continue
		}
		active, err := isActiveOrgMember(ctx, gh, org, user)
		if err != nil {
			return nil, err
		}
		if !active {
			out.pendingOrg = append(out.pendingOrg, user)
			continue
		}
		out.inviteable[user] = crRole
	}

	return out, nil
}

// rollupVerdict is the disposition of a single user from the team rollup,
// decided by classifyRollupUser.
type rollupVerdict int

const (
	// verdictNone: in sync, or an inherited member the controller must not touch.
	verdictNone rollupVerdict = iota
	// verdictRemove: a direct membership absent from the CR.
	verdictRemove
	// verdictUpdateRole: declared in the CR with a different, settable role.
	verdictUpdateRole
	// verdictRoleEnforced: declared role is overridden by GitHub org-admin enforcement.
	verdictRoleEnforced
)

// classifyRollupUser decides what to do with one user from the team rollup, given
// whether the CR declares them, their declared vs effective role, and whether they
// are inherited from a child team. The org-admin probe runs only in the two cases
// where a maintainer role could be GitHub-enforced rather than directly granted.
func classifyRollupUser(ctx context.Context, gh *ghclient.Client, org, user, ghRole, crRole string, inCR, inherited bool) (rollupVerdict, error) {
	if !inCR {
		if !inherited {
			// Direct membership, not inherited → removable.
			return verdictRemove, nil
		}
		// Inherited from a child team. A member-role entry is purely inherited, so a
		// parent-level DELETE no-ops — leave it. A maintainer-role entry is either a
		// rogue direct grant layered on the inherited membership (DELETE demotes it to
		// inherited member → converges) or an org admin (maintainer of every team;
		// DELETE can't strip it → would loop). Remove only the former.
		if ghRole != roleMaintainer {
			return verdictNone, nil
		}
		admin, err := isOrgAdmin(ctx, gh, org, user)
		if err != nil {
			return verdictNone, err
		}
		if admin {
			return verdictNone, nil
		}
		return verdictRemove, nil
	}
	// Declared in the CR.
	if crRole == ghRole {
		return verdictNone, nil
	}
	// Role mismatch. Only (GH=maintainer, CR=else) can be GitHub-enforced: GitHub
	// force-applies maintainer to org admins regardless of the declared role.
	if ghRole == roleMaintainer {
		enforced, err := isOrgAdmin(ctx, gh, org, user)
		if err != nil {
			return verdictNone, err
		}
		if enforced {
			return verdictRoleEnforced, nil
		}
	}
	return verdictUpdateRole, nil
}

const (
	orgRoleAdmin   = "admin"
	orgStateActive = "active"
)

// isOrgAdmin returns true exactly when the user is an org-level admin. False for 404 / unknown.
func isOrgAdmin(ctx context.Context, gh *ghclient.Client, org, user string) (bool, error) {
	membership, _, err := gh.Organizations.GetOrgMembership(ctx, user, org)
	if ghclient.Is404(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if membership == nil || membership.Role == nil {
		return false, nil
	}
	return *membership.Role == orgRoleAdmin, nil
}

// isActiveOrgMember returns true exactly when the user has an active org membership.
// Returns false for 404 (not a member) and for non-active states (pending
// invite). Other errors propagate.
func isActiveOrgMember(ctx context.Context, gh *ghclient.Client, org, user string) (bool, error) {
	membership, _, err := gh.Organizations.GetOrgMembership(ctx, user, org)
	if ghclient.Is404(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if membership == nil || membership.State == nil {
		return false, nil
	}
	return *membership.State == orgStateActive, nil
}

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr, ok := mg.(*v1alpha1.Team)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotTeam)
	}

	name := meta.GetExternalName(cr)
	teamSlug := slug.Make(name)

	t, _, err := c.github.Teams.GetTeamBySlug(ctx, cr.Spec.ForProvider.Org, teamSlug)
	if ghclient.Is404(err) {
		return managed.ExternalObservation{ResourceExists: false}, nil
	}
	if err != nil {
		return managed.ExternalObservation{}, err
	}

	categorized, err := categorizeMembers(ctx, c.github, cr.Spec.ForProvider.Org, teamSlug, cr.Spec.ForProvider.Members)
	if err != nil {
		return managed.ExternalObservation{}, err
	}

	crParentTeamSlug := slug.Make(pointer.Deref(cr.Spec.ForProvider.Parent, ""))
	ghParentTeamSlug := ""
	if t.Parent != nil {
		ghParentTeamSlug = pointer.Deref(t.Parent.Slug, "")
	}

	structuralDrift := crParentTeamSlug != ghParentTeamSlug ||
		pointer.Deref(cr.Spec.ForProvider.Privacy, "secret") != pointer.Deref(t.Privacy, "secret") ||
		cr.Spec.ForProvider.Description != pointer.Deref(t.Description, "")

	setTeamMembershipPartialCondition(ctx, cr, categorized.pendingOrg, categorized.pendingTeam, categorized.roleEnforced)

	if structuralDrift || categorized.hasMemberDrift() {
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

func getUserPermissionMapFromCr(users []v1alpha1.TeamMemberUser) map[string]string {
	crMToPermission := make(map[string]string, len(users))

	for _, user := range users {
		username := strings.ToLower(user.User)
		crMToPermission[username] = user.Role
	}

	return crMToPermission
}

// getPendingTeamInviteeLogins returns lowercased logins with a pending team invitation. Email-only invitations are skipped.
func getPendingTeamInviteeLogins(ctx context.Context, gh *ghclient.Client, org, slug string) ([]string, error) {
	opts := &github.ListOptions{PerPage: 100}
	var logins []string
	for {
		invitations, resp, err := gh.Teams.ListPendingTeamInvitationsBySlug(ctx, org, slug, opts)
		if err != nil {
			return nil, err
		}
		for _, inv := range invitations {
			if inv == nil || inv.Login == nil {
				continue
			}
			logins = append(logins, strings.ToLower(*inv.Login))
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return logins, nil
}

func getMembersWithPermissions(ctx context.Context, gh *ghclient.Client, org, slug string) (map[string]string, error) {
	mToPermission := make(map[string]string)
	// maintainer last: a user can be both an inherited member and a direct maintainer; querying maintainer last makes it win.
	roles := []string{roleMember, roleMaintainer}

	for _, role := range roles {
		opt := &github.TeamListTeamMembersOptions{
			Role:        role,
			ListOptions: github.ListOptions{PerPage: 100},
		}

		for {
			members, resp, err := gh.Teams.ListTeamMembersBySlug(ctx, org, slug, opt)
			if err != nil {
				return nil, err
			}

			for _, m := range members {
				if m == nil || m.Login == nil {
					continue
				}
				username := strings.ToLower(*m.Login)
				mToPermission[username] = role
			}

			if resp == nil || resp.NextPage == 0 {
				break
			}
			opt.Page = resp.NextPage
		}
	}

	return mToPermission, nil
}

// collectChildMemberLogins returns the union of logins across all direct child
// rollups. Used as an inheritance hint when categorizing parent rollup entries.
func collectChildMemberLogins(ctx context.Context, gh *ghclient.Client, org, slug string) (map[string]bool, error) {
	children, err := listChildTeamSlugs(ctx, gh, org, slug)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool)
	for _, childSlug := range children {
		childMembers, err := getMembersWithPermissions(ctx, gh, org, childSlug)
		if err != nil {
			return nil, err
		}
		for login := range childMembers {
			out[login] = true
		}
	}
	return out, nil
}

func listChildTeamSlugs(ctx context.Context, gh *ghclient.Client, org, parentSlug string) ([]string, error) {
	opts := &github.ListOptions{PerPage: 100}
	var slugs []string
	for {
		teams, resp, err := gh.Teams.ListChildTeamsByParentSlug(ctx, org, parentSlug, opts)
		if err != nil {
			return nil, err
		}
		for _, t := range teams {
			if t == nil || t.Slug == nil {
				continue
			}
			slugs = append(slugs, *t.Slug)
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return slugs, nil
}

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	cr, ok := mg.(*v1alpha1.Team)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotTeam)
	}

	name := meta.GetExternalName(cr)
	teamSlug := slug.Make(name)
	privacy := pointer.Deref(cr.Spec.ForProvider.Privacy, "secret")

	t := github.NewTeam{
		Name:        name,
		Description: &cr.Spec.ForProvider.Description,
		Privacy:     &privacy,
	}
	_, _, err := c.github.Teams.CreateTeam(ctx, cr.Spec.ForProvider.Org, t)
	if err != nil {
		return managed.ExternalCreation{}, err
	}

	if err := updateTeamUsers(ctx, cr, c.github, teamSlug); err != nil {
		return managed.ExternalCreation{}, err
	}

	return managed.ExternalCreation{}, nil
}

func updateTeamUsers(ctx context.Context, cr *v1alpha1.Team, gh *ghclient.Client, teamSlug string) error {
	categorized, err := categorizeMembers(ctx, gh, cr.Spec.ForProvider.Org, teamSlug, cr.Spec.ForProvider.Members)
	if err != nil {
		return err
	}

	for userName := range categorized.toRemove {
		_, err := gh.Teams.RemoveTeamMembershipBySlug(ctx, cr.Spec.ForProvider.Org, teamSlug, userName)
		if err != nil {
			return err
		}
	}

	// inviteable only: PUT for non-org-members would send an org-invite side effect.
	for userName, role := range categorized.inviteable {
		opt := &github.TeamAddTeamMembershipOptions{Role: role}
		if _, _, err := gh.Teams.AddTeamMembershipBySlug(ctx, cr.Spec.ForProvider.Org, teamSlug, userName, opt); err != nil {
			return err
		}
	}
	for userName, role := range categorized.roleUpdate {
		opt := &github.TeamAddTeamMembershipOptions{Role: role}
		if _, _, err := gh.Teams.AddTeamMembershipBySlug(ctx, cr.Spec.ForProvider.Org, teamSlug, userName, opt); err != nil {
			return err
		}
	}

	return nil
}

func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	cr, ok := mg.(*v1alpha1.Team)
	if !ok {
		return managed.ExternalUpdate{}, errors.New(errNotTeam)
	}

	name := meta.GetExternalName(cr)
	teamSlug := slug.Make(name)

	err := updateTeamUsers(ctx, cr, c.github, teamSlug)
	if err != nil {
		return managed.ExternalUpdate{}, err
	}

	crParentTeamSlug := ""
	parentT := &github.Team{}
	removeParent := true

	if cr.Spec.ForProvider.Parent != nil {
		crParentTeamSlug = slug.Make(*cr.Spec.ForProvider.Parent)
		parentT, _, err = c.github.Teams.GetTeamBySlug(ctx, cr.Spec.ForProvider.Org, crParentTeamSlug)
		if err != nil {
			return managed.ExternalUpdate{}, err
		}
		removeParent = false
	}

	privacy := pointer.Deref(cr.Spec.ForProvider.Privacy, "secret")
	newTeam := github.NewTeam{
		Name:        name,
		Privacy:     &privacy,
		Description: &cr.Spec.ForProvider.Description,
	}
	if !removeParent {
		newTeam.ParentTeamID = parentT.ID
	}

	_, _, err = c.github.Teams.EditTeamBySlug(ctx, cr.Spec.ForProvider.Org, teamSlug, newTeam, removeParent)
	if err != nil {
		return managed.ExternalUpdate{}, err
	}

	return managed.ExternalUpdate{}, nil
}

func (c *external) Delete(ctx context.Context, mg resource.Managed) error {
	cr, ok := mg.(*v1alpha1.Team)
	if !ok {
		return errors.New(errNotTeam)
	}

	name := meta.GetExternalName(cr)
	teamSlug := slug.Make(name)

	_, err := c.github.Teams.DeleteTeamBySlug(ctx, cr.Spec.ForProvider.Org, teamSlug)
	if err != nil {
		return err
	}

	return nil
}
