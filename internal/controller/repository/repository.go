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

package repository

import (
	stdcmp "cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"

	pointer "k8s.io/utils/ptr"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/google/go-github/v90/github"
	"github.com/gosimple/slug"

	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	"github.com/provider-github/provider-github/apis/cluster/organizations/v1alpha1"
	namespacedv1alpha1 "github.com/provider-github/provider-github/apis/namespaced/organizations/v1alpha1"
	ghclient "github.com/provider-github/provider-github/internal/clients"
	"github.com/provider-github/provider-github/internal/clients/rulesets"
	"github.com/provider-github/provider-github/internal/controller/driftcmp"
	"github.com/provider-github/provider-github/internal/controller/mgmtpolicy"
	"github.com/provider-github/provider-github/internal/telemetry"
	"github.com/provider-github/provider-github/internal/util"
)

const (
	errNotRepository = "managed resource is not a Repository custom resource"
	errTrackPCUsage  = "cannot track ProviderConfig usage"
	errGetPC         = "cannot get ProviderConfig"

	errNewClient = "cannot create new Service"
)

// forgettingFinalizer deletes a repository's gauge series once its finalizer is removed, since the Orphan policy never calls Delete.
type forgettingFinalizer struct {
	inner   resource.Finalizer
	metrics *telemetry.RateLimitMetrics
}

func (f *forgettingFinalizer) AddFinalizer(ctx context.Context, obj resource.Object) error {
	return f.inner.AddFinalizer(ctx, obj)
}

func (f *forgettingFinalizer) RemoveFinalizer(ctx context.Context, obj resource.Object) error {
	if err := f.inner.RemoveFinalizer(ctx, obj); err != nil {
		return err
	}

	if f.metrics == nil {
		return nil
	}

	switch cr := obj.(type) {
	case *v1alpha1.Repository:
		f.metrics.ForgetRepository(cr.Spec.ForProvider.Org, meta.GetExternalName(cr))
	case *namespacedv1alpha1.Repository:
		f.metrics.ForgetRepository(cr.Spec.ForProvider.Org, meta.GetExternalName(cr))
	}
	return nil
}

type external struct {
	kube    client.Client
	github  *ghclient.Client
	metrics *telemetry.RateLimitMetrics

	// secretNamespace, when set, is the only namespace webhook secrets are read
	// from, whatever namespace the spec names. Namespaced resources set it to
	// their own namespace.
	secretNamespace string
}

//nolint:gocyclo
func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr, ok := mg.(*v1alpha1.Repository)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotRepository)
	}

	name := meta.GetExternalName(cr)

	repo, _, err := c.github.Repositories.Get(ctx, cr.Spec.ForProvider.Org, name)
	if ghclient.Is404(err) {
		if c.metrics != nil {
			c.metrics.ForgetRepository(cr.Spec.ForProvider.Org, name)
		}
		return managed.ExternalObservation{ResourceExists: false}, nil
	}
	if err != nil {
		return managed.ExternalObservation{}, err
	}

	cr.Status.AtProvider.ID = name
	mirrorRepository(&cr.Status.AtProvider, repo, cr.Spec.ForProvider.Org)

	// Archived repos freeze settings, branch protection, rulesets and webhooks on
	// GitHub; only team access, topics and collaborator removals stay writable. They
	// reconcile on a separate path so frozen drift can't loop, and the freeze is
	// surfaced on the CR rather than ignored silently.
	//
	// Observe reads and mirrors every section before it reports a difference, so
	// status.atProvider shows what GitHub holds for the sections after the first
	// one that differs; drift records only that a section differed. The read path
	// follows GitHub's archived state, not the spec's.
	//
	// Under an Observe-only policy a field the spec omits is not compared: there
	// is no write that could correct it, so reporting it would hold Ready False.
	writes := mgmtpolicy.WritesDeclared(cr.GetManagementPolicies())
	drift := (cr.Spec.ForProvider.Archived != nil || writes) &&
		pointer.Deref(cr.Spec.ForProvider.Archived, false) != pointer.Deref(repo.Archived, false)
	if pointer.Deref(repo.Archived, false) {
		return c.observeArchived(ctx, cr, repo, name, drift)
	}
	setArchivedCondition(cr, false, nil)
	c.recordUnreconcilable(cr, telemetry.DimensionArchived, typeArchivedConfigFrozen)

	collaborators, err := categorizeCollaborators(ctx, c.github, cr.Spec.ForProvider.Org, name, cr.Spec.ForProvider.Permissions.Users)
	if err != nil {
		return managed.ExternalObservation{}, err
	}
	setCollaboratorPartialCondition(cr, collaborators.pendingInvite, collaborators.roleEnforced)
	c.recordUnreconcilable(cr, telemetry.DimensionCollaborators, typeCollaboratorPartial)
	cr.Status.AtProvider.Permissions.Users = mirrorUsers(collaborators.observed)
	drift = drift || ((cr.Spec.ForProvider.Permissions.Users != nil || writes) && collaborators.hasDrift())

	crTToPermission := getTeamPermissionMapFromCr(cr.Spec.ForProvider.Permissions.Teams)
	ghTToPermission, err := getRepoTeamsWithPermissions(ctx, c.github, cr.Spec.ForProvider.Org, name)
	if err != nil {
		return managed.ExternalObservation{}, err
	}
	cr.Status.AtProvider.Permissions.Teams = mirrorTeams(ghTToPermission)

	drift = drift || ((cr.Spec.ForProvider.Permissions.Teams != nil || writes) && !driftcmp.Equal(ghTToPermission, crTToPermission))

	if cr.Spec.ForProvider.Webhooks != nil {
		ghRepoWebhooks, err := getRepoWebhooks(ctx, c.github, cr.Spec.ForProvider.Org, name)
		if err != nil {
			return managed.ExternalObservation{}, err
		}

		crWToConfig, err := c.getRepoWebhooksMapFromCr(ctx, cr.Spec.ForProvider.Webhooks)
		if err != nil {
			return managed.ExternalObservation{}, err
		}

		ghWToConfig, err := c.getRepoWebhooksWithConfig(ctx, ghRepoWebhooks, cr)
		if err != nil {
			return managed.ExternalObservation{}, err
		}
		if cr.Status.AtProvider.Webhooks, err = mirrorWebhooks(ghWToConfig); err != nil {
			return managed.ExternalObservation{}, err
		}

		drift = drift || !driftcmp.Equal(ghWToConfig, crWToConfig)
	} else {
		cr.Status.AtProvider.Webhooks = nil
	}

	if cr.Spec.ForProvider.BranchProtectionRules != nil {
		protectedBranches, err := listProtectedBranches(ctx, c.github, cr.Spec.ForProvider.Org, name)
		if err != nil {
			return managed.ExternalObservation{}, err
		}
		crBPRToConfig := getBPRMapFromCr(cr.Spec.ForProvider.BranchProtectionRules)
		skipped, err := filterMissingBranchProtectionRules(ctx, c.github, cr.Spec.ForProvider.Org, name, crBPRToConfig, protectedBranchSet(protectedBranches))
		if err != nil {
			return managed.ExternalObservation{}, err
		}
		if len(skipped) > 0 {
			ctrl.LoggerFrom(ctx).Info("skipping branch protection rules for missing branches",
				"repository", name, "branches", skipped)
		}
		ghBPRToConfig, err := getBPRWithConfig(ctx, c.github, cr.Spec.ForProvider.Org, name, protectedBranches)
		if err != nil {
			return managed.ExternalObservation{}, err
		}
		if cr.Status.AtProvider.BranchProtectionRules, err = mirrorBranchProtection(ghBPRToConfig); err != nil {
			return managed.ExternalObservation{}, err
		}

		forcePushKept := forcePushKeptBranches(crBPRToConfig, ghBPRToConfig)

		records := currentUnappliedBranchProtection(cr.Status.AtProvider.UnappliedBranchProtection, crBPRToConfig)
		cr.Status.AtProvider.UnappliedBranchProtection = records

		unapplied := detectUnappliedBranchProtectionActors(crBPRToConfig, ghBPRToConfig)
		enforced, err := enforcedBranchProtectionActors(ctx, c.github, cr.Spec.ForProvider.Org, name, unapplied)
		if err != nil {
			return managed.ExternalObservation{}, err
		}
		remembered := rememberedApps(records, unapplied)
		setBranchProtectionPartialCondition(cr, branchProtectionReport{
			missingBranches: skipped,
			enforcedActors:  enforced,
			rememberedApps:  remembered,
			forcePushKept:   forcePushKept,
		})
		c.recordUnreconcilable(cr, telemetry.DimensionBranchProtection, typeBranchProtectionPartial)

		dropped := slices.Concat(enforced, remembered)
		crBPRWithoutDropped := withoutBranchProtectionActors(crBPRToConfig, dropped)
		applyRememberedForcePushes(crBPRWithoutDropped, ghBPRToConfig, records)
		drift = drift || !driftcmp.Equal(crBPRWithoutDropped, ghBPRToConfig)
	} else {
		cr.Status.AtProvider.UnappliedBranchProtection = nil
		cr.Status.AtProvider.BranchProtectionRules = nil
		setBranchProtectionPartialCondition(cr, branchProtectionReport{})
		c.recordUnreconcilable(cr, telemetry.DimensionBranchProtection, typeBranchProtectionPartial)
	}

	// Rulesets are skipped while the CR is being deleted, so the deletion goes ahead even
	// when the ruleset endpoint fails (403 on plans without private-repository rulesets).
	cr.Status.AtProvider.RepositoryRules = nil
	if cr.Spec.ForProvider.RepositoryRules != nil && !meta.WasDeleted(cr) {
		rulesDrift, err := c.observeRulesets(ctx, cr, name)
		if err != nil {
			return managed.ExternalObservation{}, err
		}
		drift = drift || rulesDrift
	} else {
		setRulesetsPartialCondition(cr, nil, nil, rulesetReport{})
		c.recordRulesetsUnreconcilable(cr)
	}

	remembered := rememberedSettings(cr.Status.AtProvider.UnappliedSettings)
	settings := pushedSettings(editRequest(cr, repo, name), repo)
	cr.Status.AtProvider.UnappliedSettings = currentUnappliedSettings(remembered, settings)
	setSettingsPartialCondition(cr, cr.Status.AtProvider.UnappliedSettings)
	c.recordUnreconcilable(cr, telemetry.DimensionSettings, typeSettingsPartial)

	for _, setting := range settings {
		if settingRemembered(remembered, setting.field, setting.requested) {
			continue
		}
		if setting.requested != setting.echoed {
			drift = true
			break
		}
	}

	// Check topics
	if cr.Spec.ForProvider.Topics != nil {
		crTopics := util.SortAndReturn(cr.Spec.ForProvider.Topics)
		ghTopics := util.SortAndReturn(repo.Topics)

		drift = drift || !driftcmp.Equal(crTopics, ghTopics)
	}

	if drift {
		return drifted(cr), nil
	}

	cr.SetConditions(xpv2.Available())

	return managed.ExternalObservation{
		ResourceExists:   true,
		ResourceUpToDate: true,
	}, nil
}

// drifted reports an existing Repository that differs from its spec. Ready is
// False until a later poll finds it in sync; the update that corrects the drift
// runs in the meantime.
func drifted(cr *v1alpha1.Repository) managed.ExternalObservation {
	cr.SetConditions(xpv2.Unavailable())
	return managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: false}
}

// Condition surfaced when a repo is archived. GitHub makes archived repos
// read-only for settings, branch protection, rulesets, webhooks and collaborator
// additions, so the controller cannot reconcile those; the condition states this
// rather than letting the skipped reconciliation go unnoticed on the CR.
const (
	typeArchivedConfigFrozen xpv2.ConditionType   = "ArchivedConfigFrozen"
	reasonRepositoryArchived xpv2.ConditionReason = "RepositoryArchived"
	reasonNotArchived        xpv2.ConditionReason = "NotArchived"
)

// setArchivedCondition reports the frozen dimensions while archived, listing any
// declared collaborators that can't be added because the repo is archived. When
// not archived it clears a previously-set condition (no-op if never set).
func setArchivedCondition(cr *v1alpha1.Repository, archived bool, skippedAdds []string) {
	if !archived {
		if cr.GetCondition(typeArchivedConfigFrozen).Status == corev1.ConditionTrue {
			cr.SetConditions(xpv2.Condition{
				Type:               typeArchivedConfigFrozen,
				Status:             corev1.ConditionFalse,
				Reason:             reasonNotArchived,
				LastTransitionTime: metav1.Now(),
			})
		}
		return
	}
	msg := "repository is archived; settings, branch protection, rulesets and webhooks are not reconciled"
	if len(skippedAdds) > 0 {
		sort.Strings(skippedAdds)
		msg += "; collaborators cannot be added while archived: " + strings.Join(skippedAdds, ", ")
	}
	cr.SetConditions(xpv2.Condition{
		Type:               typeArchivedConfigFrozen,
		Status:             corev1.ConditionTrue,
		Reason:             reasonRepositoryArchived,
		Message:            msg,
		LastTransitionTime: metav1.Now(),
	})
}

// Condition surfaced while a ruleset in the spec lists the same ref pattern or bypass
// actor twice, while GitHub answers the ruleset list with 403 on a repository that
// declares rulesets, such as a private one whose plan has no rulesets, or while a
// managed ruleset holds rule types or parameters the provider does not model. An
// invalid spec leaves the list unread and a 403 leaves nothing fetched, so either
// reason stands alone. While rulesets hold both unmanaged rule types and unmanaged
// parameters, the reason is UnmanagedRuleTypes and the message names both.
const (
	typeRulesetsPartial       xpv2.ConditionType   = "RulesetsPartial"
	reasonInvalidRulesets     xpv2.ConditionReason = "InvalidRulesets"
	reasonRulesetsForbidden   xpv2.ConditionReason = "RulesetsForbidden"
	reasonUnmanagedRuleTypes  xpv2.ConditionReason = "UnmanagedRuleTypes"
	reasonUnmanagedParameters xpv2.ConditionReason = "UnmanagedParameters"
	reasonRulesetsReconciled  xpv2.ConditionReason = "RulesetsReconciled"
)

// rulesetReport lists, sorted, one sentence per managed ruleset left as it is because
// it holds rule types the provider does not model, and one per rule holding
// parameters the provider does not model. skipped holds the names of those rulesets
// left as they are, sorted. extraApprovalReturned holds, sorted, the names of the
// rulesets whose pull_request rule GitHub answered with
// requireExtraApprovalForUnattributedChanges while the CR leaves it unset; the desired
// value is GitHub's default, true.
type rulesetReport struct {
	unmanagedTypes        []string
	unmanagedParameters   []string
	skipped               []string
	extraApprovalReturned []string
	// observed holds, keyed by name, every ruleset of the repository as GitHub holds it, in CR
	// form with the rule types and parameters the provider models, whether or not the
	// ruleset is compared. A ruleset the CR does not name is listed by name only.
	observed map[string]v1alpha1.RepositoryRuleset
}

// applyRulesetReport brings the CR's rulesets, as getRepositoryRulesMapFromCr returns
// them, to the desired state compared with and sent to GitHub: the rulesets in
// report.skipped drop out, and the pull_request rule of the rulesets in
// report.extraApprovalReturned gets requireExtraApprovalForUnattributedChanges true.
// Rules.PullRequest is a pointer held by the map entry, so the value set through it is
// in crRulesets.
func applyRulesetReport(crRulesets map[string]v1alpha1.RepositoryRuleset, report rulesetReport) {
	for _, name := range report.skipped {
		delete(crRulesets, name)
	}
	for _, name := range report.extraApprovalReturned {
		crRulesets[name].Rules.PullRequest.RequireExtraApprovalForUnattributedChanges = pointer.To(true)
	}
}

// setRulesetsPartialCondition carries the message from invalid, the error naming the
// duplicate in the spec, GitHub's message from forbidden, the 403 of the ruleset list,
// or the sentences of report, unmanaged rule types first, in that order, and reports
// False when there are none.
func setRulesetsPartialCondition(cr *v1alpha1.Repository, invalid, forbidden error, report rulesetReport) {
	c := xpv2.Condition{Type: typeRulesetsPartial, LastTransitionTime: metav1.Now()}
	switch {
	case invalid != nil:
		c.Status = corev1.ConditionTrue
		c.Reason = reasonInvalidRulesets
		c.Message = invalid.Error()
	case forbidden != nil:
		msg := forbidden.Error()
		var errResp *github.ErrorResponse
		if errors.As(forbidden, &errResp) {
			msg = errResp.Message
		}
		c.Status = corev1.ConditionTrue
		c.Reason = reasonRulesetsForbidden
		c.Message = "GitHub answers the ruleset list with 403, so rulesets are left as they are: " + msg
	case len(report.unmanagedTypes) > 0:
		c.Status = corev1.ConditionTrue
		c.Reason = reasonUnmanagedRuleTypes
		c.Message = strings.Join(slices.Concat(report.unmanagedTypes, report.unmanagedParameters), "; ")
	case len(report.unmanagedParameters) > 0:
		c.Status = corev1.ConditionTrue
		c.Reason = reasonUnmanagedParameters
		c.Message = strings.Join(report.unmanagedParameters, "; ")
	default:
		c.Status = corev1.ConditionFalse
		c.Reason = reasonRulesetsReconciled
	}
	cr.SetConditions(c)
}

// recordRulesetsUnreconcilable publishes the ruleset_spec, rulesets and ruleset_parameters
// dimensions, each from its reasons of the RulesetsPartial condition.
func (c *external) recordRulesetsUnreconcilable(cr *v1alpha1.Repository) {
	c.recordUnreconcilable(cr, telemetry.DimensionRulesetSpec, typeRulesetsPartial, reasonInvalidRulesets)
	c.recordUnreconcilable(cr, telemetry.DimensionRulesetsForbidden, typeRulesetsPartial, reasonRulesetsForbidden)
	c.recordUnreconcilable(cr, telemetry.DimensionRulesetParameters, typeRulesetsPartial, reasonUnmanagedRuleTypes, reasonUnmanagedParameters)
}

// recordUnreconcilable publishes the dimension's gauge from its condition's current status.
// With a reason given, the dimension is unreconcilable only while the condition holds it.
func (c *external) recordUnreconcilable(cr *v1alpha1.Repository, dimension string, conditionType xpv2.ConditionType, reason ...xpv2.ConditionReason) {
	if c.metrics == nil {
		return
	}
	condition := cr.GetCondition(conditionType)
	unreconcilable := condition.Status == corev1.ConditionTrue && (len(reason) == 0 || slices.Contains(reason, condition.Reason))
	c.metrics.SetRepositoryUnreconcilable(cr.Spec.ForProvider.Org, meta.GetExternalName(cr), dimension, unreconcilable)
}

// mirrorFrozenSections reads webhooks, branch protection and rulesets, which
// GitHub refuses to write while the repository is archived but still returns, and
// mirrors them into status.atProvider. Each section is read only while the spec
// declares it, as when the repository is not archived, and cleared otherwise.
// None of them takes part in drift: they cannot be reconciled while archived.
func (c *external) mirrorFrozenSections(ctx context.Context, cr *v1alpha1.Repository, name string) error {
	org := cr.Spec.ForProvider.Org

	cr.Status.AtProvider.Webhooks = nil
	if cr.Spec.ForProvider.Webhooks != nil {
		hooks, err := getRepoWebhooks(ctx, c.github, org, name)
		if err != nil {
			return err
		}
		ghWToConfig, err := c.getRepoWebhooksWithConfig(ctx, hooks, cr)
		if err != nil {
			return err
		}
		if cr.Status.AtProvider.Webhooks, err = mirrorWebhooks(ghWToConfig); err != nil {
			return err
		}
	}

	cr.Status.AtProvider.BranchProtectionRules = nil
	if cr.Spec.ForProvider.BranchProtectionRules != nil {
		protectedBranches, err := listProtectedBranches(ctx, c.github, org, name)
		if err != nil {
			return err
		}
		ghBPRToConfig, err := getBPRWithConfig(ctx, c.github, org, name, protectedBranches)
		if err != nil {
			return err
		}
		if cr.Status.AtProvider.BranchProtectionRules, err = mirrorBranchProtection(ghBPRToConfig); err != nil {
			return err
		}
	}

	cr.Status.AtProvider.RepositoryRules = nil
	if cr.Spec.ForProvider.RepositoryRules != nil {
		// As when not archived, a failed list reads as no rulesets: an organization
		// whose plan has no rulesets answers the list with an error.
		ghRulesets, _ := getRepositoryRules(ctx, c.github, org, name)
		// Every ruleset is fetched, and one holding rule types this provider does not
		// manage is mirrored with the rule types it models: only a write needs it
		// left alone.
		named := make(map[string]v1alpha1.RepositoryRuleset, len(ghRulesets))
		for _, r := range ghRulesets {
			named[r.Name] = v1alpha1.RepositoryRuleset{Name: r.Name}
		}
		_, report, err := getRepositoryRulesWithConfig(ctx, c.github, org, name, ghRulesets, named)
		if err != nil {
			return err
		}
		if cr.Status.AtProvider.RepositoryRules, err = mirrorRulesets(report.observed); err != nil {
			return err
		}
	}
	return nil
}

// observeRulesets reads the repository's rulesets, mirrors them into
// status.atProvider and reports whether they differ from the spec. A spec that lists
// the same ref pattern or bypass actor twice, or a 403 on the ruleset list, leaves the
// rulesets unread and is reported through the RulesetsPartial condition, so the other
// sections still decide whether the repository is up to date.
func (c *external) observeRulesets(ctx context.Context, cr *v1alpha1.Repository, name string) (bool, error) {
	crRulesets, invalid := getRepositoryRulesMapFromCr(*cr.Spec.ForProvider.RepositoryRules)
	if invalid != nil {
		setRulesetsPartialCondition(cr, invalid, nil, rulesetReport{})
		c.recordRulesetsUnreconcilable(cr)
		return false, nil
	}

	ghRulesets, err := getRepositoryRules(ctx, c.github, cr.Spec.ForProvider.Org, name)
	if err != nil && !ghclient.Is403(err) {
		return false, err
	}
	if err != nil {
		// The refusal leaves the declared rulesets unapplied; a repository that declares
		// none already has the state it asks for.
		if len(*cr.Spec.ForProvider.RepositoryRules) > 0 {
			setRulesetsPartialCondition(cr, nil, err, rulesetReport{})
		} else {
			setRulesetsPartialCondition(cr, nil, nil, rulesetReport{})
		}
		c.recordRulesetsUnreconcilable(cr)
		return false, nil
	}

	ghRulesetsToConfig, report, err := getRepositoryRulesWithConfig(ctx, c.github, cr.Spec.ForProvider.Org, name, ghRulesets, crRulesets)
	if err != nil {
		return false, err
	}
	setRulesetsPartialCondition(cr, nil, nil, report)
	c.recordRulesetsUnreconcilable(cr)
	if cr.Status.AtProvider.RepositoryRules, err = mirrorRulesets(report.observed); err != nil {
		return false, err
	}

	applyRulesetReport(crRulesets, report)
	return !driftcmp.Equal(crRulesets, ghRulesetsToConfig), nil
}

// observeArchived reports drift for an archived repo. Only team access, topics and
// collaborator removals are reconcilable while archived; settings, branch protection,
// rulesets, webhooks and collaborator additions are frozen and surfaced via a
// condition. The frozen sections are read and mirrored, but never compared. drift
// is true when an earlier check, the spec's archived flag against GitHub's, already
// differed.
func (c *external) observeArchived(ctx context.Context, cr *v1alpha1.Repository, repo *github.Repository, name string, drift bool) (managed.ExternalObservation, error) {
	org := cr.Spec.ForProvider.Org

	crUsers := getUserPermissionMapFromCr(cr.Spec.ForProvider.Permissions.Users)
	ghUsers, err := getRepoUsersWithPermissions(ctx, c.github, org, name)
	if err != nil {
		return managed.ExternalObservation{}, err
	}
	cr.Status.AtProvider.Permissions.Users = mirrorUsers(ghUsers)
	removable, toAdd, toUpdate := util.DiffPermissions(ghUsers, crUsers)
	skippedAdds := make([]string, 0, len(toAdd)+len(toUpdate))
	for u := range util.MergeMaps(toAdd, toUpdate) {
		skippedAdds = append(skippedAdds, u)
	}

	// Neither is reconciled while archived; skipped adds are named in the archived condition.
	setCollaboratorPartialCondition(cr, nil, nil)
	c.recordUnreconcilable(cr, telemetry.DimensionCollaborators, typeCollaboratorPartial)
	// The unapplied records hold writes that were attempted and not applied, not
	// what GitHub has. Nothing is written while archived, so both stay empty.
	cr.Status.AtProvider.UnappliedBranchProtection = nil
	cr.Status.AtProvider.UnappliedSettings = nil
	setBranchProtectionPartialCondition(cr, branchProtectionReport{})
	c.recordUnreconcilable(cr, telemetry.DimensionBranchProtection, typeBranchProtectionPartial)
	setSettingsPartialCondition(cr, nil)
	c.recordUnreconcilable(cr, telemetry.DimensionSettings, typeSettingsPartial)
	setRulesetsPartialCondition(cr, nil, nil, rulesetReport{})
	c.recordRulesetsUnreconcilable(cr)

	setArchivedCondition(cr, true, skippedAdds)
	c.recordUnreconcilable(cr, telemetry.DimensionArchived, typeArchivedConfigFrozen)

	if err := c.mirrorFrozenSections(ctx, cr, name); err != nil {
		return managed.ExternalObservation{}, err
	}

	crTeams := getTeamPermissionMapFromCr(cr.Spec.ForProvider.Permissions.Teams)
	ghTeams, err := getRepoTeamsWithPermissions(ctx, c.github, org, name)
	if err != nil {
		return managed.ExternalObservation{}, err
	}
	cr.Status.AtProvider.Permissions.Teams = mirrorTeams(ghTeams)
	writes := mgmtpolicy.WritesDeclared(cr.GetManagementPolicies())
	teamsDrift := (cr.Spec.ForProvider.Permissions.Teams != nil || writes) && !driftcmp.Equal(ghTeams, crTeams)

	topicsDrift := false
	if cr.Spec.ForProvider.Topics != nil {
		topicsDrift = !driftcmp.Equal(util.SortAndReturn(cr.Spec.ForProvider.Topics), util.SortAndReturn(repo.Topics))
	}

	usersDrift := (cr.Spec.ForProvider.Permissions.Users != nil || writes) && len(removable) > 0
	if drift || usersDrift || teamsDrift || topicsDrift {
		return drifted(cr), nil
	}

	cr.SetConditions(xpv2.Available())
	return managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: true}, nil
}

func getTeamPermissionMapFromCr(teams []v1alpha1.RepositoryTeam) map[string]string {
	crTToPermission := make(map[string]string, len(teams))
	for _, team := range teams {
		teamSlug := slug.Make(team.Team)
		crTToPermission[teamSlug] = team.Role
	}

	return crTToPermission
}

func getUserPermissionMapFromCr(users []v1alpha1.RepositoryUser) map[string]string {
	crMToPermission := make(map[string]string, len(users))

	for _, user := range users {
		username := strings.ToLower(user.User)
		crMToPermission[username] = user.Role
	}

	return crMToPermission
}

func (c *external) getRepoWebhooksMapFromCr(ctx context.Context, webhooks []v1alpha1.RepositoryWebhook) (map[string]v1alpha1.RepositoryWebhook, error) {
	crWToConfig := make(map[string]v1alpha1.RepositoryWebhook, len(webhooks))

	for i := range webhooks {
		webhook := webhooks[i]
		// handle optional *bool fields
		insecureSsl := util.BoolDerefToPointer(webhook.InsecureSSL, false)
		active := util.BoolDerefToPointer(webhook.Active, true)

		secret, err := c.getRepoWebhookSecretFromRef(ctx, &webhook)
		if err != nil {
			return nil, err
		}

		// sort events to aid comparison between desired and actual state
		sort.Strings(webhook.Events)

		crWToConfig[webhook.URL] = v1alpha1.RepositoryWebhook{
			URL:         webhook.URL,
			InsecureSSL: insecureSsl,
			ContentType: webhook.ContentType,
			Events:      webhook.Events,
			Active:      active,
			Secret:      secret,
		}
	}
	return crWToConfig, nil
}

func (c *external) getRepoWebhookSecretFromRef(ctx context.Context, webhook *v1alpha1.RepositoryWebhook) (secret *string, err error) {
	if webhook.SecretKeyRef == nil {
		return nil, nil
	}

	secretKey := webhook.SecretKeyRef.Key
	secretName := webhook.SecretKeyRef.Name
	secretNamespace := webhook.SecretKeyRef.Namespace
	if c.secretNamespace != "" {
		secretNamespace = c.secretNamespace
	}

	nn := types.NamespacedName{
		Name:      secretName,
		Namespace: secretNamespace,
	}

	s := &corev1.Secret{}
	if err := c.kube.Get(ctx, nn, s); err != nil {
		return nil, errors.Errorf("cannot get secret `%s/%s`", secretNamespace, secretName)
	}

	// Check if `secretKey` key exists in secret data
	data, ok := s.Data[secretKey]
	if !ok {
		return nil, errors.Errorf("secret key `%s` not found in secret `%s/%s`", secretKey, secretNamespace, secretName)
	}

	secretValue := string(data)
	return &secretValue, nil
}

type webhookSecretState struct {
	WebhookUrl    string `json:"webhookUrl"`
	WebhookSecret string `json:"webhookSecret"`
}

func (c *external) getRepoWebhookSecretFromState(ctx context.Context, cr *v1alpha1.Repository, webhook *github.Hook) (secret string, err error) {
	if cr.Spec.WriteConnectionSecretToReference == nil {
		return "", errors.New("`spec.writeConnectionSecretToReference` is not set")
	}

	secretKey := util.GenerateSHA1Hash(webhook.Config.GetURL())
	secretName := cr.Spec.WriteConnectionSecretToReference.Name
	secretNamespace := cr.Spec.WriteConnectionSecretToReference.Namespace
	nn := types.NamespacedName{
		Name:      secretName,
		Namespace: secretNamespace,
	}
	s := &corev1.Secret{}

	// the output secret may not exist yet, so we can skip returning an error if the error is `NotFound`
	if err := c.kube.Get(ctx, nn, s); resource.IgnoreNotFound(err) != nil {
		return "", err
	}

	// Check if `secretKey` key exists in secret data
	secretValue, ok := s.Data[secretKey]

	// It's fine to return no error if `secretKey` doesn't exist in secret data, maybe the secret hasn't been updated yet
	if !ok {
		return "", nil
	}

	var data webhookSecretState
	err = json.Unmarshal(secretValue, &data)
	if err != nil {
		return "", errors.Errorf("cannot unmarshal secret value for secret key `%s` in secret `%s/%s`", secretKey, secretNamespace, secretName)
	}
	return data.WebhookSecret, nil
}

func (c *external) updateConnectionSecretEntry(ctx context.Context, cr *v1alpha1.Repository, secretKey string, secretValue webhookSecretState) error {
	if cr.Spec.WriteConnectionSecretToReference == nil {
		return errors.New("cannot update connection secret, `spec.writeConnectionSecretToReference` is not set")
	}

	secretName := cr.Spec.WriteConnectionSecretToReference.Name
	secretNamespace := cr.Spec.WriteConnectionSecretToReference.Namespace
	nn := types.NamespacedName{
		Name:      secretName,
		Namespace: secretNamespace,
	}

	s := &corev1.Secret{}
	if err := c.kube.Get(ctx, nn, s); err != nil {
		return err
	}

	// Ensure the secret's Data field is initialized
	if s.Data == nil {
		s.Data = make(map[string][]byte)
	}

	// Update the secret with the provided key and value
	bytes, err := json.Marshal(secretValue)
	if err != nil {
		return err
	}
	s.Data[secretKey] = bytes

	// Apply the update to the Kubernetes API
	if err := c.kube.Update(ctx, s); err != nil {
		return err
	}
	return nil
}

func (c *external) deleteConnectionSecretEntry(ctx context.Context, cr *v1alpha1.Repository, secretKey string) error {
	if cr.Spec.WriteConnectionSecretToReference == nil {
		return errors.New("cannot update connection secret, `spec.writeConnectionSecretToReference` is not set")
	}

	secretName := cr.Spec.WriteConnectionSecretToReference.Name
	secretNamespace := cr.Spec.WriteConnectionSecretToReference.Namespace
	nn := types.NamespacedName{
		Name:      secretName,
		Namespace: secretNamespace,
	}
	s := &corev1.Secret{}

	// the output secret may not exist yet, so we can skip returning an error if the error is `NotFound`
	if err := c.kube.Get(ctx, nn, s); resource.IgnoreNotFound(err) != nil {
		return err
	}

	// Check if `secretKey` key exists in secret data and delete it if it exists
	_, ok := s.Data[secretKey]
	if ok {
		delete(s.Data, secretKey)
		if err := c.kube.Update(ctx, s); err != nil {
			return errors.Errorf("cannot delete secret entry for `%s` in repository connection secret %s/%s", secretKey, secretNamespace, secretName)
		}
	}
	return nil
}

func getRepoWebhooks(ctx context.Context, gh *ghclient.Client, org, repoName string) ([]*github.Hook, error) {
	opt := &github.ListOptions{PerPage: 100}
	var allHooks []*github.Hook

	for {
		hooks, resp, err := gh.Repositories.ListHooks(ctx, org, repoName, opt)
		if err != nil {
			return nil, err
		}
		allHooks = append(allHooks, hooks...)

		if resp.NextPage == 0 {
			break
		}
		opt.Page = resp.NextPage
	}

	return allHooks, nil
}

func (c *external) getRepoWebhooksWithConfig(ctx context.Context, hooks []*github.Hook, repo *v1alpha1.Repository) (map[string]v1alpha1.RepositoryWebhook, error) {
	wToConfig := make(map[string]v1alpha1.RepositoryWebhook)

	for _, h := range hooks {
		url := h.Config.GetURL()
		contentType := h.Config.GetContentType()
		insecureSslBool := false
		if h.Config.InsecureSSL != nil && *h.Config.InsecureSSL == "1" {
			insecureSslBool = true
		}
		var secret *string
		if h.Config.Secret != nil {
			var err error
			secretValue, err := c.getRepoWebhookSecretFromState(ctx, repo, h)
			if err != nil {
				return nil, err
			}
			secret = &secretValue
		}
		wToConfig[url] = v1alpha1.RepositoryWebhook{
			URL:         url,
			InsecureSSL: &insecureSslBool,
			ContentType: contentType,
			Events:      h.Events,
			Active:      h.Active,
			Secret:      secret,
		}
	}

	return wToConfig, nil

}

func getRepoWebhookId(hooks []*github.Hook, webhookUrl string) (*int64, error) {

	for _, h := range hooks {
		if h.Config.GetURL() == webhookUrl {
			return h.ID, nil
		}
	}

	return nil, errors.Errorf("cannot find repository webhook id for %s", webhookUrl)
}

func getRepoTeamsWithPermissions(ctx context.Context, gh *ghclient.Client, org, name string) (map[string]string, error) {
	tToPermission := make(map[string]string)

	opt := &github.ListOptions{PerPage: 100}

	for {
		repos, resp, err := gh.Repositories.ListTeams(ctx, org, name, opt)
		if err != nil {
			return nil, err
		}

		for _, m := range repos {
			tToPermission[*m.Slug] = *m.Permission
		}

		if resp.NextPage == 0 {
			break
		}
		opt.Page = resp.NextPage
	}

	return tToPermission, nil
}

func getRepoUsersWithPermissions(ctx context.Context, gh *ghclient.Client, org, name string) (map[string]string, error) {
	uToPermission := make(map[string]string)

	opt := &github.ListCollaboratorsOptions{
		Affiliation: "direct",
		ListOptions: github.ListOptions{PerPage: 100},
	}

	for {
		users, resp, err := gh.Repositories.ListCollaborators(ctx, org, name, opt)
		if err != nil {
			return nil, err
		}

		for _, m := range users {
			username := strings.ToLower(*m.Login)

			perms := m.GetPermissions()
			switch {
			case perms.GetAdmin():
				uToPermission[username] = repoPermissionAdmin
			case perms.GetMaintain():
				uToPermission[username] = repoPermissionMaintain
			case perms.GetPush():
				uToPermission[username] = repoPermissionPush
			case perms.GetTriage():
				uToPermission[username] = repoPermissionTriage
			default:
				uToPermission[username] = repoPermissionPull
			}
		}

		if resp.NextPage == 0 {
			break
		}
		opt.Page = resp.NextPage
	}

	return uToPermission, nil
}

// listProtectedBranches returns every protected branch on a repo,
// iterating pages. The Protected=true filter on GitHub keeps the
// page count tiny (typically 1 page) regardless of how many feature
// branches the repo has, so per-Observe pagination cost stays
// constant even on monorepos with thousands of branches. The filter also
// matches branches protected only by a ruleset, so a listed branch may have
// no classic protection (see getBPRWithConfig).
func listProtectedBranches(ctx context.Context, gh *ghclient.Client, org, repoName string) ([]*github.Branch, error) {
	opts := &github.BranchListOptions{
		Protected:   github.Ptr(true),
		ListOptions: github.ListOptions{PerPage: 100},
	}
	var protected []*github.Branch
	for {
		branches, resp, err := gh.Repositories.ListBranches(ctx, org, repoName, opts)
		if err != nil {
			return nil, err
		}
		protected = append(protected, branches...)
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return protected, nil
}

// 1 hop covers a single branch rename; deeper chains aren't worth chasing.
const branchRenameRedirectsToFollow = 1

// filterMissingBranchProtectionRules removes entries from rules whose
// target branch doesn't exist in the repo. Returns the sorted list of
// skipped branch names so callers can log and emit an Event. Mutates
// rules.
//
// Branches already in protectedSet exist by construction (you cannot
// protect a branch that doesn't exist), so they're accepted without
// any extra API call. The rest are confirmed with a per-branch
// Repositories.GetBranch — 404 means the branch is missing and the
// rule is dropped; a redirect to a different name means the branch
// was renamed (mutating endpoints don't follow redirects, so the rule
// can't apply against the old name) and the skipped entry carries the
// new name so the operator can update the CR; any other error
// propagates.
func filterMissingBranchProtectionRules(ctx context.Context, gh *ghclient.Client, org, repoName string, rules map[string]v1alpha1.BranchProtectionRule, protectedSet map[string]bool) ([]string, error) {
	var skipped []string
	for branch := range rules {
		if protectedSet[branch] {
			continue
		}
		// GetBranch reports non-200 with a bare fmt.Errorf, so check resp.StatusCode rather than Is404(err).
		got, resp, err := gh.Repositories.GetBranch(ctx, org, repoName, branch, branchRenameRedirectsToFollow)
		if err == nil {
			if got != nil && got.GetName() != branch {
				skipped = append(skipped, fmt.Sprintf("%s (was renamed to %s)", branch, got.GetName()))
				delete(rules, branch)
			}
			continue
		}
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			skipped = append(skipped, branch)
			delete(rules, branch)
			continue
		}
		return nil, err
	}
	sort.Strings(skipped)
	return skipped, nil
}

// protectedBranchSet returns the set of branch names from a list of
// protected branches, for fast "is this branch already protected?"
// lookup in filterMissingBranchProtectionRules.
func protectedBranchSet(branches []*github.Branch) map[string]bool {
	set := make(map[string]bool, len(branches))
	for _, b := range branches {
		set[b.GetName()] = true
	}
	return set
}

// One condition for every declared branch protection item GitHub did not apply.
const (
	typeBranchProtectionPartial xpv2.ConditionType   = "BranchProtectionPartial"
	reasonNotFullyApplied       xpv2.ConditionReason = "NotFullyApplied"
	reasonFullyApplied          xpv2.ConditionReason = "FullyApplied"
)

// branchProtectionReport lists the declared branch protection GitHub did not apply.
type branchProtectionReport struct {
	missingBranches []string
	enforcedActors  []branchProtectionActorRef
	rememberedApps  []branchProtectionActorRef
	forcePushKept   []string
}

// Branches declared without force pushes that GitHub keeps enabled (per-actor allowance not settable via REST).
func forcePushKeptBranches(crBPR, ghBPR map[string]v1alpha1.BranchProtectionRule) []string {
	var branches []string
	for branch, want := range crBPR {
		got, ok := ghBPR[branch]
		if !ok {
			continue
		}
		if !pointer.Deref(want.AllowForcePushes, false) && pointer.Deref(got.AllowForcePushes, false) {
			branches = append(branches, branch)
		}
	}
	sort.Strings(branches)
	return branches
}

// Idempotent: SetConditions ignores writes whose (Status, Reason, Message) are unchanged.
func setBranchProtectionPartialCondition(cr *v1alpha1.Repository, report branchProtectionReport) {
	var segments []string
	if len(report.missingBranches) > 0 {
		segments = append(segments, "branches do not exist in repo: "+strings.Join(report.missingBranches, ", "))
	}
	if len(report.enforcedActors) > 0 {
		segments = append(segments, "actors lack write access on the repo, so GitHub drops them from branch protection: "+strings.Join(branchProtectionActorNames(report.enforcedActors), ", "))
	}
	if len(report.rememberedApps) > 0 {
		segments = append(segments, "actors GitHub did not store on the last push (apps need contents write): "+strings.Join(branchProtectionActorNames(report.rememberedApps), ", "))
	}
	if len(report.forcePushKept) > 0 {
		segments = append(segments, "force pushes stay enabled because a per-actor force-push allowance is set in the GitHub UI, which the REST API cannot change: "+strings.Join(report.forcePushKept, ", "))
	}

	c := xpv2.Condition{
		Type:               typeBranchProtectionPartial,
		LastTransitionTime: metav1.Now(),
	}
	if len(segments) == 0 {
		c.Status = corev1.ConditionFalse
		c.Reason = reasonFullyApplied
	} else {
		c.Status = corev1.ConditionTrue
		c.Reason = reasonNotFullyApplied
		c.Message = strings.Join(segments, "; ")
	}
	cr.SetConditions(c)
}

func branchProtectionActorNames(refs []branchProtectionActorRef) []string {
	names := make([]string, len(refs))
	for i, ref := range refs {
		names[i] = fmt.Sprintf("%s/%s:%s", ref.branch, ref.field, ref.actor)
	}
	return names
}

// GitHub drops actors lacking write access with a 200, so Observe treats their absence as enforced.

// Field tokens for the "branch/field:actor" names the condition reports.
const (
	fieldBypassUsers      = "bypassUsers"
	fieldBypassTeams      = "bypassTeams"
	fieldBypassApps       = "bypassApps"
	fieldDismissalUsers   = "dismissalUsers"
	fieldDismissalTeams   = "dismissalTeams"
	fieldDismissalApps    = "dismissalApps"
	fieldRestrictionUsers = "restrictionUsers"
	fieldRestrictionTeams = "restrictionTeams"
	fieldRestrictionApps  = "restrictionApps"
)

// branchProtectionActorRef identifies one actor within a branch protection rule.
type branchProtectionActorRef struct {
	branch string
	field  string
	actor  string
}

// branchProtectionActorRefs flattens every bypass/dismissal/restriction actor in a BPR map.
func branchProtectionActorRefs(m map[string]v1alpha1.BranchProtectionRule) map[branchProtectionActorRef]bool {
	out := map[branchProtectionActorRef]bool{}
	add := func(branch, field string, actors []string) {
		for _, a := range actors {
			out[branchProtectionActorRef{branch: branch, field: field, actor: a}] = true
		}
	}
	for branch, rule := range m {
		if rpr := rule.RequiredPullRequestReviews; rpr != nil {
			if bp := rpr.BypassPullRequestAllowances; bp != nil {
				add(branch, fieldBypassUsers, bp.Users)
				add(branch, fieldBypassTeams, bp.Teams)
				add(branch, fieldBypassApps, bp.Apps)
			}
			if dr := rpr.DismissalRestrictions; dr != nil {
				add(branch, fieldDismissalUsers, pointer.Deref(dr.Users, nil))
				add(branch, fieldDismissalTeams, pointer.Deref(dr.Teams, nil))
				add(branch, fieldDismissalApps, pointer.Deref(dr.Apps, nil))
			}
		}
		if r := rule.BranchProtectionRestrictions; r != nil {
			add(branch, fieldRestrictionUsers, r.Users)
			add(branch, fieldRestrictionTeams, r.Teams)
			add(branch, fieldRestrictionApps, r.Apps)
		}
	}
	return out
}

// Sorted so the condition message is stable across reconciles.
func detectUnappliedBranchProtectionActors(declared, stored map[string]v1alpha1.BranchProtectionRule) []branchProtectionActorRef {
	declaredRefs := branchProtectionActorRefs(declared)
	storedRefs := branchProtectionActorRefs(stored)

	dropped := make([]branchProtectionActorRef, 0, len(declaredRefs))
	for ref := range declaredRefs {
		if storedRefs[ref] {
			continue
		}
		dropped = append(dropped, ref)
	}
	sortBranchProtectionActorRefs(dropped)
	return dropped
}

func sortBranchProtectionActorRefs(refs []branchProtectionActorRef) {
	sort.Slice(refs, func(i, j int) bool {
		a, b := refs[i], refs[j]
		if a.branch != b.branch {
			return a.branch < b.branch
		}
		if a.field != b.field {
			return a.field < b.field
		}
		return a.actor < b.actor
	})
}

// Apps are never enforced: GitHub exposes no permission to probe.
func enforcedBranchProtectionActors(ctx context.Context, gh *ghclient.Client, owner, repo string, unapplied []branchProtectionActorRef) ([]branchProtectionActorRef, error) {
	userCanWrite := map[string]bool{}
	teamCanWrite := map[string]bool{}
	enforced := make([]branchProtectionActorRef, 0, len(unapplied))

	for _, ref := range unapplied {
		switch ref.field {
		case fieldBypassUsers, fieldDismissalUsers, fieldRestrictionUsers:
			if _, probed := userCanWrite[ref.actor]; !probed {
				ok, err := userHasWriteAccess(ctx, gh, owner, repo, ref.actor)
				if err != nil {
					return nil, err
				}
				userCanWrite[ref.actor] = ok
			}
			if !userCanWrite[ref.actor] {
				enforced = append(enforced, ref)
			}
		case fieldBypassTeams, fieldDismissalTeams, fieldRestrictionTeams:
			if _, probed := teamCanWrite[ref.actor]; !probed {
				ok, err := teamHasWriteAccess(ctx, gh, owner, ref.actor, owner, repo)
				if err != nil {
					return nil, err
				}
				teamCanWrite[ref.actor] = ok
			}
			if !teamCanWrite[ref.actor] {
				enforced = append(enforced, ref)
			}
		}
	}
	return enforced, nil
}

// Repository permission levels reported by the GitHub API.
const (
	repoPermissionAdmin    = "admin"
	repoPermissionMaintain = "maintain"
	repoPermissionWrite    = "write"
	repoPermissionPush     = "push"
	repoPermissionTriage   = "triage"
	repoPermissionPull     = "pull"
)

// 404 (not a collaborator) counts as no access.
func userHasWriteAccess(ctx context.Context, gh *ghclient.Client, owner, repo, user string) (bool, error) {
	level, _, err := gh.Repositories.GetPermissionLevel(ctx, owner, repo, user)
	if ghclient.Is404(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	permission := level.GetPermission()
	return permission == repoPermissionAdmin || permission == repoPermissionWrite, nil
}

// Probed because a team inheriting access from its parent is absent from the repo team list.
func teamHasWriteAccess(ctx context.Context, gh *ghclient.Client, org, slug, owner, repo string) (bool, error) {
	teamRepo, _, err := gh.Teams.IsTeamRepoBySlug(ctx, org, slug, owner, repo)
	if ghclient.Is404(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	permissions := teamRepo.GetPermissions()
	return permissions.GetPush() || permissions.GetMaintain() || permissions.GetAdmin(), nil
}

func withoutBranchProtectionActors(rules map[string]v1alpha1.BranchProtectionRule, drop []branchProtectionActorRef) map[string]v1alpha1.BranchProtectionRule {
	dropSet := make(map[branchProtectionActorRef]bool, len(drop))
	for _, ref := range drop {
		dropSet[ref] = true
	}

	out := make(map[string]v1alpha1.BranchProtectionRule, len(rules))
	for branch, rule := range rules {
		r := rule.DeepCopy()

		if rpr := r.RequiredPullRequestReviews; rpr != nil {
			if bp := rpr.BypassPullRequestAllowances; bp != nil {
				actorsBefore := len(bp.Users) + len(bp.Teams) + len(bp.Apps)
				bp.Users = removeActors(bp.Users, branch, fieldBypassUsers, dropSet)
				bp.Teams = removeActors(bp.Teams, branch, fieldBypassTeams, dropSet)
				bp.Apps = removeActors(bp.Apps, branch, fieldBypassApps, dropSet)
				actorsAfter := len(bp.Users) + len(bp.Teams) + len(bp.Apps)
				// GitHub omits the bypass object entirely once it holds no actors.
				if actorsBefore > 0 && actorsAfter == 0 {
					rpr.BypassPullRequestAllowances = nil
				}
			}
			if dr := rpr.DismissalRestrictions; dr != nil {
				dr.Users = removeActorsPtr(dr.Users, branch, fieldDismissalUsers, dropSet)
				dr.Teams = removeActorsPtr(dr.Teams, branch, fieldDismissalTeams, dropSet)
				dr.Apps = removeActorsPtr(dr.Apps, branch, fieldDismissalApps, dropSet)
			}
		}

		if restr := r.BranchProtectionRestrictions; restr != nil {
			restr.Users = removeActors(restr.Users, branch, fieldRestrictionUsers, dropSet)
			restr.Teams = removeActors(restr.Teams, branch, fieldRestrictionTeams, dropSet)
			restr.Apps = removeActors(restr.Apps, branch, fieldRestrictionApps, dropSet)
		}

		out[branch] = *r
	}
	return out
}

// An emptied list becomes nil because getBPRWithConfig reports no actors as nil.
func removeActors(actors []string, branch, field string, dropSet map[branchProtectionActorRef]bool) []string {
	kept := make([]string, 0, len(actors))
	for _, a := range actors {
		ref := branchProtectionActorRef{branch: branch, field: field, actor: a}
		if !dropSet[ref] {
			kept = append(kept, a)
		}
	}

	// An untouched list keeps its shape (nil or empty), so steady-state comparison is unchanged.
	if len(kept) == len(actors) {
		return actors
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

func removeActorsPtr(actors *[]string, branch, field string, dropSet map[branchProtectionActorRef]bool) *[]string {
	if actors == nil {
		return nil
	}
	kept := removeActors(*actors, branch, field, dropSet)
	if kept == nil {
		return nil
	}
	return &kept
}

// Item token for a declared allow_force_pushes=false that GitHub kept enabled.
const itemAllowForcePushes = "allowForcePushes"

// ruleHash fingerprints a declared rule so a record only applies to the rule it was observed against.
// The fingerprint is taken over hashedRule rather than the rule itself, so that it stays what it was
// when a record written by an earlier release was hashed.
func ruleHash(rule v1alpha1.BranchProtectionRule) string {
	encoded, _ := json.Marshal(newHashedRule(rule)) // a plain struct always encodes
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:8])
}

// hashedRule is the encoding of a BranchProtectionRule that ruleHash fingerprints: the rule as it
// was encoded before its actor lists were encoded as null when unset. Do not add fields or change
// tags, or every stored fingerprint stops matching.
type hashedRule struct {
	Branch                         string                         `json:"branch"`
	RequiredStatusChecks           *v1alpha1.RequiredStatusChecks `json:"requiredStatusChecks,omitempty"`
	RequiredPullRequestReviews     *hashedReviews                 `json:"requiredPullRequestReviews,omitempty"`
	BranchProtectionRestrictions   *hashedRestrictions            `json:"branchProtectionRestrictions,omitempty"`
	EnforceAdmins                  bool                           `json:"enforceAdmins"`
	RequireLinearHistory           *bool                          `json:"requireLinearHistory,omitempty"`
	AllowForcePushes               *bool                          `json:"allowForcePushes,omitempty"`
	AllowDeletions                 *bool                          `json:"allowDeletions,omitempty"`
	RequiredConversationResolution *bool                          `json:"requiredConversationResolution,omitempty"`
	LockBranch                     *bool                          `json:"lockBranch,omitempty"`
	AllowForkSyncing               *bool                          `json:"allowForkSyncing,omitempty"`
	RequireSignedCommits           *bool                          `json:"requireSignedCommits,omitempty"`
}

type hashedReviews struct {
	DismissStaleReviews          bool             `json:"dismissStaleReviews"`
	RequireCodeOwnerReviews      bool             `json:"requireCodeOwnerReviews"`
	RequiredApprovingReviewCount int              `json:"requiredApprovingReviewCount"`
	RequireLastPushApproval      *bool            `json:"requireLastPushApproval,omitempty"`
	BypassPullRequestAllowances  *hashedActors    `json:"bypassPullRequestAllowances,omitempty"`
	DismissalRestrictions        *hashedDismissal `json:"dismissalRestrictions,omitempty"`
}

type hashedActors struct {
	Users []string `json:"users,omitempty"`
	Teams []string `json:"teams,omitempty"`
	Apps  []string `json:"apps,omitempty"`
}

type hashedDismissal struct {
	Users *[]string `json:"users,omitempty"`
	Teams *[]string `json:"teams,omitempty"`
	Apps  *[]string `json:"apps,omitempty"`
}

type hashedRestrictions struct {
	BlockCreations *bool    `json:"blockCreations,omitempty"`
	Users          []string `json:"users,omitempty"`
	Teams          []string `json:"teams,omitempty"`
	Apps           []string `json:"apps,omitempty"`
}

func newHashedRule(r v1alpha1.BranchProtectionRule) hashedRule {
	h := hashedRule{
		Branch:                         r.Branch,
		RequiredStatusChecks:           r.RequiredStatusChecks,
		EnforceAdmins:                  r.EnforceAdmins,
		RequireLinearHistory:           r.RequireLinearHistory,
		AllowForcePushes:               r.AllowForcePushes,
		AllowDeletions:                 r.AllowDeletions,
		RequiredConversationResolution: r.RequiredConversationResolution,
		LockBranch:                     r.LockBranch,
		AllowForkSyncing:               r.AllowForkSyncing,
		RequireSignedCommits:           r.RequireSignedCommits,
	}
	if rr := r.BranchProtectionRestrictions; rr != nil {
		h.BranchProtectionRestrictions = &hashedRestrictions{BlockCreations: rr.BlockCreations, Users: rr.Users, Teams: rr.Teams, Apps: rr.Apps}
	}
	if pr := r.RequiredPullRequestReviews; pr != nil {
		h.RequiredPullRequestReviews = &hashedReviews{
			DismissStaleReviews:          pr.DismissStaleReviews,
			RequireCodeOwnerReviews:      pr.RequireCodeOwnerReviews,
			RequiredApprovingReviewCount: pr.RequiredApprovingReviewCount,
			RequireLastPushApproval:      pr.RequireLastPushApproval,
		}
		if b := pr.BypassPullRequestAllowances; b != nil {
			h.RequiredPullRequestReviews.BypassPullRequestAllowances = &hashedActors{Users: b.Users, Teams: b.Teams, Apps: b.Apps}
		}
		if d := pr.DismissalRestrictions; d != nil {
			h.RequiredPullRequestReviews.DismissalRestrictions = &hashedDismissal{Users: d.Users, Teams: d.Teams, Apps: d.Apps}
		}
	}
	return h
}

func isAppField(field string) bool {
	return field == fieldBypassApps || field == fieldDismissalApps || field == fieldRestrictionApps
}

// unappliedItems lists the declared apps and force-push setting the echoed rule left out, sorted.
func unappliedItems(declared, echoed v1alpha1.BranchProtectionRule) []string {
	declaredRefs := branchProtectionActorRefs(map[string]v1alpha1.BranchProtectionRule{declared.Branch: declared})
	echoedRefs := branchProtectionActorRefs(map[string]v1alpha1.BranchProtectionRule{declared.Branch: echoed})

	items := make([]string, 0, len(declaredRefs)+1)
	for ref := range declaredRefs {
		if !isAppField(ref.field) || echoedRefs[ref] {
			continue
		}
		items = append(items, ref.field+":"+ref.actor)
	}
	if !pointer.Deref(declared.AllowForcePushes, false) && pointer.Deref(echoed.AllowForcePushes, false) {
		items = append(items, itemAllowForcePushes)
	}
	sort.Strings(items)
	return items
}

// setUnappliedBranchProtection replaces the record for rule's branch; no items drops it.
func setUnappliedBranchProtection(cr *v1alpha1.Repository, rule v1alpha1.BranchProtectionRule, items []string) {
	var records []v1alpha1.UnappliedBranchProtection
	for _, record := range cr.Status.AtProvider.UnappliedBranchProtection {
		if record.Branch != rule.Branch {
			records = append(records, record)
		}
	}
	if len(items) > 0 {
		records = append(records, v1alpha1.UnappliedBranchProtection{
			Branch:   rule.Branch,
			RuleHash: ruleHash(rule),
			Items:    items,
		})
	}
	// Sorted so the status is stable across reconciles.
	sort.Slice(records, func(i, j int) bool { return records[i].Branch < records[j].Branch })
	cr.Status.AtProvider.UnappliedBranchProtection = records
}

// currentUnappliedBranchProtection keeps the records whose branch is still declared with the same rule.
func currentUnappliedBranchProtection(records []v1alpha1.UnappliedBranchProtection, declared map[string]v1alpha1.BranchProtectionRule) []v1alpha1.UnappliedBranchProtection {
	current := make([]v1alpha1.UnappliedBranchProtection, 0, len(records))
	for _, record := range records {
		rule, ok := declared[record.Branch]
		if !ok || record.RuleHash != ruleHash(rule) {
			continue
		}
		current = append(current, record)
	}
	return current
}

// rememberedApps returns the unapplied app actors a current record accounts for.
func rememberedApps(records []v1alpha1.UnappliedBranchProtection, unapplied []branchProtectionActorRef) []branchProtectionActorRef {
	recorded := map[branchProtectionActorRef]bool{}
	for _, record := range records {
		for _, item := range record.Items {
			field, actor, isActor := strings.Cut(item, ":")
			if isActor {
				recorded[branchProtectionActorRef{branch: record.Branch, field: field, actor: actor}] = true
			}
		}
	}

	var remembered []branchProtectionActorRef
	for _, ref := range unapplied {
		if isAppField(ref.field) && recorded[ref] {
			remembered = append(remembered, ref)
		}
	}
	return remembered
}

// applyRememberedForcePushes takes GitHub's force-push setting for branches whose record says it was not applied.
func applyRememberedForcePushes(rules, stored map[string]v1alpha1.BranchProtectionRule, records []v1alpha1.UnappliedBranchProtection) {
	for _, record := range records {
		if !slices.Contains(record.Items, itemAllowForcePushes) {
			continue
		}
		rule, declared := rules[record.Branch]
		got, protected := stored[record.Branch]
		if !declared || !protected {
			continue
		}
		// The record only means "declared off, kept on"; any other pair is ordinary drift.
		if pointer.Deref(rule.AllowForcePushes, false) || !pointer.Deref(got.AllowForcePushes, false) {
			continue
		}
		rule.AllowForcePushes = got.AllowForcePushes
		rules[record.Branch] = rule
	}
}

// getBPRMapFromCr generates a map from a slice of BranchProtectionRules. Each rule is first processed:
// sorts the RequiredStatusChecks and any checks in various rule sub-structures, then the updated rule
// is added to the map with its branch name as the key. The function returns the resulting map.
//
//nolint:gocyclo
func getBPRMapFromCr(rules []v1alpha1.BranchProtectionRule) map[string]v1alpha1.BranchProtectionRule {
	crBPRToConfig := make(map[string]v1alpha1.BranchProtectionRule, len(rules))

	for i := range rules {
		// Use a copy to avoid changing passed []v1alpha1.BranchProtectionRule
		// This prevents the controller from changing the spec of the live CR
		// It can also prevent infinite reconciliation loops when managing the resources with ArgoCD
		orig := &rules[i]
		rCopy := orig.DeepCopy()

		// handle optional *bool fields
		rCopy.RequireLinearHistory = util.BoolDerefToPointer(rCopy.RequireLinearHistory, false)
		rCopy.AllowForcePushes = util.BoolDerefToPointer(rCopy.AllowForcePushes, false)
		rCopy.AllowDeletions = util.BoolDerefToPointer(rCopy.AllowDeletions, false)
		rCopy.RequiredConversationResolution = util.BoolDerefToPointer(rCopy.RequiredConversationResolution, false)
		rCopy.LockBranch = util.BoolDerefToPointer(rCopy.LockBranch, false)
		rCopy.AllowForkSyncing = util.BoolDerefToPointer(rCopy.AllowForkSyncing, false)
		rCopy.RequireSignedCommits = util.BoolDerefToPointer(rCopy.RequireSignedCommits, false)

		if rCopy.RequiredStatusChecks != nil && rCopy.RequiredStatusChecks.Checks != nil {
			copyOfStatusChecks := make([]*v1alpha1.RequiredStatusCheck, len(rCopy.RequiredStatusChecks.Checks))
			copy(copyOfStatusChecks, rCopy.RequiredStatusChecks.Checks)
			util.SortRequiredStatusChecks(copyOfStatusChecks)
			rCopy.RequiredStatusChecks.Checks = copyOfStatusChecks
		}

		restr := rCopy.BranchProtectionRestrictions
		if restr != nil {
			restr.BlockCreations = util.BoolDerefToPointer(restr.BlockCreations, false)
			if restr.Users != nil {
				restr.Users = util.SortAndReturn(util.ToLowerSlice(restr.Users))
			}
			if restr.Teams != nil {
				restr.Teams = util.SortAndReturn(util.ToLowerSlice(restr.Teams))
			}
			if restr.Apps != nil {
				restr.Apps = util.SortAndReturn(util.ToLowerSlice(restr.Apps))
			}
		}

		rPRs := rCopy.RequiredPullRequestReviews
		if rPRs != nil {
			// handle optional *bool fields
			rPRs.RequireLastPushApproval = util.BoolDerefToPointer(rPRs.RequireLastPushApproval, false)

			allowances := rPRs.BypassPullRequestAllowances
			if allowances != nil {
				if allowances.Users != nil {
					allowances.Users = util.SortAndReturn(util.ToLowerSlice(allowances.Users))
				}
				if allowances.Teams != nil {
					allowances.Teams = util.SortAndReturn(util.ToLowerSlice(allowances.Teams))
				}
				if allowances.Apps != nil {
					allowances.Apps = util.SortAndReturn(util.ToLowerSlice(allowances.Apps))
				}
			}
			dismissal := rPRs.DismissalRestrictions
			if dismissal != nil {
				if dismissal.Users != nil {
					dismissal.Users = util.SortAndReturnPointer(util.ToLowerSlice(*dismissal.Users))
				}
				if dismissal.Teams != nil {
					dismissal.Teams = util.SortAndReturnPointer(util.ToLowerSlice(*dismissal.Teams))
				}
				if dismissal.Apps != nil {
					dismissal.Apps = util.SortAndReturnPointer(util.ToLowerSlice(*dismissal.Apps))
				}
			}
		}

		crBPRToConfig[rCopy.Branch] = *rCopy
	}

	return crBPRToConfig
}

// getBPRWithConfig creates a map of BranchProtectionRules for a GitHub repository based on its branches' current protection settings.
// It fetches each branch's protection settings from GitHub and maps them to BranchProtectionRule objects.
// Any lists of users, teams, or apps in the rules are sorted.
// GitHub's protected-branch listing also returns branches guarded only by a ruleset; the classic
// endpoint answers those with "Branch not protected". Such a branch has no classic rule, so it is
// left out of the map (like an unprotected branch) rather than failing the whole read. Any other
// error is returned.
func getBPRWithConfig(ctx context.Context, gh *ghclient.Client, owner, repo string, branches []*github.Branch) (map[string]v1alpha1.BranchProtectionRule, error) {
	bprToConfig := make(map[string]v1alpha1.BranchProtectionRule, len(branches))

	for _, branch := range branches {
		protection, _, err := gh.Repositories.GetBranchProtection(ctx, owner, repo, branch.GetName())
		if errors.Is(err, github.ErrBranchNotProtected) {
			continue
		}
		if err != nil {
			return nil, err
		}
		bprToConfig[branch.GetName()] = protectionToRule(branch.GetName(), protection)
	}
	return bprToConfig, nil
}

// protectionToRule maps GitHub's protection of branch to a BranchProtectionRule, with actor lists sorted.
//
//nolint:gocyclo
func protectionToRule(branch string, protection *github.Protection) v1alpha1.BranchProtectionRule {
	bpr := v1alpha1.BranchProtectionRule{
		Branch:                         branch,
		EnforceAdmins:                  protection.GetEnforceAdmins().Enabled,
		RequireLinearHistory:           &protection.GetRequireLinearHistory().Enabled,
		AllowForcePushes:               &protection.GetAllowForcePushes().Enabled,
		AllowDeletions:                 &protection.GetAllowDeletions().Enabled,
		RequiredConversationResolution: &protection.GetRequiredConversationResolution().Enabled,
		LockBranch:                     pointer.To(protection.GetLockBranch().GetEnabled()),
		AllowForkSyncing:               pointer.To(protection.GetAllowForkSyncing().GetEnabled()),
		RequireSignedCommits:           pointer.To(protection.GetRequiredSignatures().GetEnabled()),
	}

	rChecks := protection.GetRequiredStatusChecks()
	if rChecks != nil {
		bpr.RequiredStatusChecks = &v1alpha1.RequiredStatusChecks{
			Strict: rChecks.Strict,
		}
		if rChecks.Checks != nil && len(*rChecks.Checks) > 0 {
			checks := make([]*v1alpha1.RequiredStatusCheck, len(*rChecks.Checks))
			for i, check := range *rChecks.Checks {
				checks[i] = &v1alpha1.RequiredStatusCheck{
					Context: check.Context,
					AppID:   check.AppID,
				}
			}
			util.SortRequiredStatusChecks(checks)
			bpr.RequiredStatusChecks.Checks = checks
		}
	}

	rPRs := protection.GetRequiredPullRequestReviews()
	if rPRs != nil {
		bpr.RequiredPullRequestReviews = &v1alpha1.RequiredPullRequestReviews{
			DismissStaleReviews:          rPRs.DismissStaleReviews,
			RequireCodeOwnerReviews:      rPRs.RequireCodeOwnerReviews,
			RequiredApprovingReviewCount: rPRs.RequiredApprovingReviewCount,
			RequireLastPushApproval:      &rPRs.RequireLastPushApproval,
		}

		dismissal := rPRs.GetDismissalRestrictions()
		if dismissal != nil {
			bpr.RequiredPullRequestReviews.DismissalRestrictions = &v1alpha1.DismissalRestrictionsRequest{}
			if len(dismissal.Users) > 0 {
				users := make([]string, len(dismissal.Users))
				for i, user := range dismissal.Users {
					users[i] = user.GetLogin()
				}
				bpr.RequiredPullRequestReviews.DismissalRestrictions.Users = util.SortAndReturnPointer(util.ToLowerSlice(users))
			}
			if len(dismissal.Teams) > 0 {
				teams := make([]string, len(dismissal.Teams))
				for i, team := range dismissal.Teams {
					teams[i] = team.GetSlug()
				}
				bpr.RequiredPullRequestReviews.DismissalRestrictions.Teams = util.SortAndReturnPointer(util.ToLowerSlice(teams))
			}
			if len(dismissal.Apps) > 0 {
				apps := make([]string, len(dismissal.Apps))
				for i, app := range dismissal.Apps {
					apps[i] = app.GetSlug()
				}
				bpr.RequiredPullRequestReviews.DismissalRestrictions.Apps = util.SortAndReturnPointer(util.ToLowerSlice(apps))
			}
		}

		allowances := rPRs.GetBypassPullRequestAllowances()
		if allowances != nil {
			bpr.RequiredPullRequestReviews.BypassPullRequestAllowances = &v1alpha1.BypassPullRequestAllowancesRequest{}
			if len(allowances.Users) > 0 {
				users := make([]string, len(allowances.Users))
				for i, user := range allowances.Users {
					users[i] = user.GetLogin()
				}
				bpr.RequiredPullRequestReviews.BypassPullRequestAllowances.Users = util.SortAndReturn(util.ToLowerSlice(users))
			}
			if len(allowances.Teams) > 0 {
				teams := make([]string, len(allowances.Teams))
				for i, team := range allowances.Teams {
					teams[i] = team.GetSlug()
				}
				bpr.RequiredPullRequestReviews.BypassPullRequestAllowances.Teams = util.SortAndReturn(util.ToLowerSlice(teams))
			}
			if len(allowances.Apps) > 0 {
				apps := make([]string, len(allowances.Apps))
				for i, app := range allowances.Apps {
					apps[i] = app.GetSlug()
				}
				bpr.RequiredPullRequestReviews.BypassPullRequestAllowances.Apps = util.SortAndReturn(util.ToLowerSlice(apps))
			}
		}
	}

	restr := protection.GetRestrictions()
	if restr != nil {
		bpr.BranchProtectionRestrictions = &v1alpha1.BranchProtectionRestrictions{}
		bpr.BranchProtectionRestrictions.BlockCreations = pointer.To(protection.GetBlockCreations().GetEnabled())
		if len(restr.Users) > 0 {
			users := make([]string, len(restr.Users))
			for i, user := range restr.Users {
				users[i] = user.GetLogin()
			}
			bpr.BranchProtectionRestrictions.Users = util.SortAndReturn(util.ToLowerSlice(users))
		}
		if len(restr.Teams) > 0 {
			teams := make([]string, len(restr.Teams))
			for i, team := range restr.Teams {
				teams[i] = team.GetSlug()
			}
			bpr.BranchProtectionRestrictions.Teams = util.SortAndReturn(util.ToLowerSlice(teams))
		}
		if len(restr.Apps) > 0 {
			apps := make([]string, len(restr.Apps))
			for i, app := range restr.Apps {
				apps[i] = app.GetSlug()
			}
			bpr.BranchProtectionRestrictions.Apps = util.SortAndReturn(util.ToLowerSlice(apps))
		}
	}

	return bpr
}

// applyMainSettings copies the optional main-settings fields from spec into req when set.
func applyMainSettings(req *github.Repository, cr *v1alpha1.Repository) {
	fp := cr.Spec.ForProvider
	if fp.DefaultBranch != nil {
		req.DefaultBranch = fp.DefaultBranch
	}
	if fp.AllowMergeCommit != nil {
		req.AllowMergeCommit = fp.AllowMergeCommit
	}
	if fp.AllowSquashMerge != nil {
		req.AllowSquashMerge = fp.AllowSquashMerge
	}
	if fp.AllowRebaseMerge != nil {
		req.AllowRebaseMerge = fp.AllowRebaseMerge
	}
	if fp.AllowAutoMerge != nil {
		req.AllowAutoMerge = fp.AllowAutoMerge
	}
	if fp.AllowUpdateBranch != nil {
		req.AllowUpdateBranch = fp.AllowUpdateBranch
	}
	if fp.DeleteBranchOnMerge != nil {
		req.DeleteBranchOnMerge = fp.DeleteBranchOnMerge
	}
	if fp.HasIssues != nil {
		req.HasIssues = fp.HasIssues
	}
	if fp.HasProjects != nil {
		req.HasProjects = fp.HasProjects
	}
	if fp.HasWiki != nil {
		req.HasWiki = fp.HasWiki
	}
	if fp.HasDiscussions != nil {
		req.HasDiscussions = fp.HasDiscussions
	}
	if fp.MergeCommitTitle != nil {
		req.MergeCommitTitle = fp.MergeCommitTitle
	}
	if fp.MergeCommitMessage != nil {
		req.MergeCommitMessage = fp.MergeCommitMessage
	}
	if fp.SquashMergeCommitTitle != nil {
		req.SquashMergeCommitTitle = fp.SquashMergeCommitTitle
	}
	if fp.SquashMergeCommitMessage != nil {
		req.SquashMergeCommitMessage = fp.SquashMergeCommitMessage
	}
}

// Spec field names of the repository settings Update pushes through Repositories.Edit.
const (
	settingDescription              = "description"
	settingPrivate                  = "private"
	settingIsTemplate               = "isTemplate"
	settingDefaultBranch            = "defaultBranch"
	settingAllowMergeCommit         = "allowMergeCommit"
	settingAllowSquashMerge         = "allowSquashMerge"
	settingAllowRebaseMerge         = "allowRebaseMerge"
	settingAllowAutoMerge           = "allowAutoMerge"
	settingAllowUpdateBranch        = "allowUpdateBranch"
	settingDeleteBranchOnMerge      = "deleteBranchOnMerge"
	settingHasIssues                = "hasIssues"
	settingHasProjects              = "hasProjects"
	settingHasWiki                  = "hasWiki"
	settingHasDiscussions           = "hasDiscussions"
	settingMergeCommitTitle         = "mergeCommitTitle"
	settingMergeCommitMessage       = "mergeCommitMessage"
	settingSquashMergeCommitTitle   = "squashMergeCommitTitle"
	settingSquashMergeCommitMessage = "squashMergeCommitMessage"
)

// Condition surfaced when GitHub answers a settings push with 200 but keeps other values (plan or repository type).
const typeSettingsPartial xpv2.ConditionType = "SettingsPartial"

// editRequest builds the settings Edit Update sends; Observe compares the same request against GitHub.
// Under an Observe-only policy a description, isTemplate or private the spec
// omits is left out of the request, so Observe does not compare it.
func editRequest(cr *v1alpha1.Repository, repo *github.Repository, name string) *github.Repository {
	writes := mgmtpolicy.WritesDeclared(cr.GetManagementPolicies())
	archived := pointer.Deref(cr.Spec.ForProvider.Archived, false)
	req := &github.Repository{
		Name:     &name,
		Archived: &archived,
	}
	if cr.Spec.ForProvider.Description != "" || writes {
		req.Description = &cr.Spec.ForProvider.Description
	}
	if cr.Spec.ForProvider.IsTemplate != nil || writes {
		isTemplate := pointer.Deref(cr.Spec.ForProvider.IsTemplate, false)
		req.IsTemplate = &isTemplate
	}
	// Visibility can't be changed on a fork.
	if !pointer.Deref(repo.Fork, false) && (cr.Spec.ForProvider.Private != nil || writes) {
		private := pointer.Deref(cr.Spec.ForProvider.Private, true)
		req.Private = &private
	}
	applyMainSettings(req, cr)
	return req
}

// pushedSetting is one setting Update sent, with the value GitHub echoed back.
type pushedSetting struct {
	field     string
	requested string
	echoed    string
}

func appendPushedBool(settings []pushedSetting, field string, requested, echoed *bool) []pushedSetting {
	if requested == nil {
		return settings
	}
	return append(settings, pushedSetting{
		field:     field,
		requested: strconv.FormatBool(*requested),
		echoed:    strconv.FormatBool(pointer.Deref(echoed, false)),
	})
}

func appendPushedString(settings []pushedSetting, field string, requested, echoed *string) []pushedSetting {
	if requested == nil {
		return settings
	}
	return append(settings, pushedSetting{
		field:     field,
		requested: *requested,
		echoed:    pointer.Deref(echoed, ""),
	})
}

// pushedSettings lists the settings req set that Observe compares; name and archived are left out.
func pushedSettings(req, echoed *github.Repository) []pushedSetting {
	var settings []pushedSetting
	settings = appendPushedString(settings, settingDescription, req.Description, echoed.Description)
	settings = appendPushedBool(settings, settingPrivate, req.Private, echoed.Private)
	settings = appendPushedBool(settings, settingIsTemplate, req.IsTemplate, echoed.IsTemplate)
	settings = appendPushedString(settings, settingDefaultBranch, req.DefaultBranch, echoed.DefaultBranch)
	settings = appendPushedBool(settings, settingAllowMergeCommit, req.AllowMergeCommit, echoed.AllowMergeCommit)
	settings = appendPushedBool(settings, settingAllowSquashMerge, req.AllowSquashMerge, echoed.AllowSquashMerge)
	settings = appendPushedBool(settings, settingAllowRebaseMerge, req.AllowRebaseMerge, echoed.AllowRebaseMerge)
	settings = appendPushedBool(settings, settingAllowAutoMerge, req.AllowAutoMerge, echoed.AllowAutoMerge)
	settings = appendPushedBool(settings, settingAllowUpdateBranch, req.AllowUpdateBranch, echoed.AllowUpdateBranch)
	settings = appendPushedBool(settings, settingDeleteBranchOnMerge, req.DeleteBranchOnMerge, echoed.DeleteBranchOnMerge)
	settings = appendPushedBool(settings, settingHasIssues, req.HasIssues, echoed.HasIssues)
	settings = appendPushedBool(settings, settingHasProjects, req.HasProjects, echoed.HasProjects)
	settings = appendPushedBool(settings, settingHasWiki, req.HasWiki, echoed.HasWiki)
	settings = appendPushedBool(settings, settingHasDiscussions, req.HasDiscussions, echoed.HasDiscussions)
	settings = appendPushedString(settings, settingMergeCommitTitle, req.MergeCommitTitle, echoed.MergeCommitTitle)
	settings = appendPushedString(settings, settingMergeCommitMessage, req.MergeCommitMessage, echoed.MergeCommitMessage)
	settings = appendPushedString(settings, settingSquashMergeCommitTitle, req.SquashMergeCommitTitle, echoed.SquashMergeCommitTitle)
	settings = appendPushedString(settings, settingSquashMergeCommitMessage, req.SquashMergeCommitMessage, echoed.SquashMergeCommitMessage)
	return settings
}

// unappliedSettings lists the pushed settings GitHub echoed back with another value, sorted by field.
func unappliedSettings(req, echoed *github.Repository) []v1alpha1.UnappliedSetting {
	pushed := pushedSettings(req, echoed)
	records := make([]v1alpha1.UnappliedSetting, 0, len(pushed))
	for _, setting := range pushed {
		if setting.requested == setting.echoed {
			continue
		}
		records = append(records, v1alpha1.UnappliedSetting{Field: setting.field, Declared: setting.requested})
	}
	if len(records) == 0 {
		return nil
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Field < records[j].Field })
	return records
}

// rememberedSettings maps each recorded field to the declared value GitHub refused.
func rememberedSettings(records []v1alpha1.UnappliedSetting) map[string]string {
	remembered := make(map[string]string, len(records))
	for _, record := range records {
		remembered[record.Field] = record.Declared
	}
	return remembered
}

// settingRemembered reports whether the refusal was recorded against the field's current declared value.
func settingRemembered(remembered map[string]string, field, currentDeclared string) bool {
	declared, ok := remembered[field]
	return ok && declared == currentDeclared
}

// currentUnappliedSettings keeps the records whose field is still declared with the refused value, sorted by field.
func currentUnappliedSettings(remembered map[string]string, settings []pushedSetting) []v1alpha1.UnappliedSetting {
	var records []v1alpha1.UnappliedSetting
	for _, setting := range settings {
		if settingRemembered(remembered, setting.field, setting.requested) {
			records = append(records, v1alpha1.UnappliedSetting{Field: setting.field, Declared: setting.requested})
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Field < records[j].Field })
	return records
}

// Idempotent: SetConditions ignores writes whose (Status, Reason, Message) are unchanged.
func setSettingsPartialCondition(cr *v1alpha1.Repository, records []v1alpha1.UnappliedSetting) {
	c := xpv2.Condition{Type: typeSettingsPartial, LastTransitionTime: metav1.Now()}
	if len(records) == 0 {
		c.Status = corev1.ConditionFalse
		c.Reason = reasonFullyApplied
		cr.SetConditions(c)
		return
	}
	pairs := make([]string, len(records))
	for i, record := range records {
		pairs[i] = record.Field + "=" + record.Declared
	}
	c.Status = corev1.ConditionTrue
	c.Reason = reasonNotFullyApplied
	c.Message = "settings GitHub did not apply on the last push (not available on this plan or repository type): " + strings.Join(pairs, ", ")
	cr.SetConditions(c)
}

//nolint:gocyclo
func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	cr, ok := mg.(*v1alpha1.Repository)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotRepository)
	}

	name := meta.GetExternalName(cr)

	// handle optional *bool fields
	privateCr := pointer.Deref(cr.Spec.ForProvider.Private, true)

	var err error
	switch {
	case cr.Spec.ForProvider.CreateFork != nil:
		owner := cr.Spec.ForProvider.CreateFork.Owner
		repo := cr.Spec.ForProvider.CreateFork.Repo
		_, _, err = c.github.Repositories.CreateFork(ctx, owner, repo, &github.RepositoryCreateForkOptions{
			Organization:      cr.Spec.ForProvider.Org,
			Name:              name,
			DefaultBranchOnly: cr.Spec.ForProvider.CreateFork.DefaultBranchOnly,
		})
	case cr.Spec.ForProvider.CreateFromTemplate != nil:
		templateOwner := cr.Spec.ForProvider.CreateFromTemplate.Owner
		templateRepo := cr.Spec.ForProvider.CreateFromTemplate.Repo
		_, _, err = c.github.Repositories.CreateFromTemplate(ctx, templateOwner, templateRepo, github.TemplateRepoRequest{
			Name:               name,
			Owner:              &cr.Spec.ForProvider.Org,
			Description:        &cr.Spec.ForProvider.Description,
			IncludeAllBranches: &cr.Spec.ForProvider.CreateFromTemplate.IncludeAllBranches,
			Private:            &privateCr,
		})
	default:
		createReq := &github.Repository{
			Name:        &name,
			Description: &cr.Spec.ForProvider.Description,
			Private:     &privateCr,
		}
		applyMainSettings(createReq, cr)
		_, _, err = c.github.Repositories.Create(ctx, cr.Spec.ForProvider.Org, createReq)
	}

	if err != nil {
		return managed.ExternalCreation{}, err
	}

	if cr.Spec.ForProvider.Permissions.Users != nil {
		for _, user := range cr.Spec.ForProvider.Permissions.Users {
			opt := &github.RepositoryAddCollaboratorOptions{Permission: user.Role}
			_, _, err := c.github.Repositories.AddCollaborator(ctx, cr.Spec.ForProvider.Org, name, user.User, opt)
			if err != nil {
				return managed.ExternalCreation{}, err
			}
		}
	}

	if cr.Spec.ForProvider.Permissions.Teams != nil {
		for _, team := range cr.Spec.ForProvider.Permissions.Teams {
			teamSlug := slug.Make(team.Team)
			opt := &github.TeamAddTeamRepoOptions{Permission: team.Role}
			_, err := c.github.Teams.AddTeamRepoBySlug(ctx, cr.Spec.ForProvider.Org, teamSlug, cr.Spec.ForProvider.Org, name, opt)
			if err != nil {
				return managed.ExternalCreation{}, err
			}
		}
	}

	if cr.Spec.ForProvider.Webhooks != nil {
		// getRepoWebhooksMapFromCr() provides defaults for optional *bool fields
		hooksMap, err := c.getRepoWebhooksMapFromCr(ctx, cr.Spec.ForProvider.Webhooks)
		if err != nil {
			return managed.ExternalCreation{}, err
		}
		for key := range hooksMap {
			// avoid "G601: Implicit memory aliasing in for loop"
			hook := hooksMap[key]
			hookConfig := crRepoHookToHookConfig(hook)
			_, _, err = c.github.Repositories.CreateHook(ctx, cr.Spec.ForProvider.Org, name, hookConfig)
			if err != nil {
				return managed.ExternalCreation{}, err
			}
			if hookConfig.Config.Secret != nil {
				err = c.updateConnectionSecretEntry(ctx, cr, util.GenerateSHA1Hash(hook.URL), webhookSecretState{
					WebhookUrl:    *hookConfig.Config.URL,
					WebhookSecret: *hookConfig.Config.Secret,
				})
				if err != nil {
					return managed.ExternalCreation{}, err
				}
			}
		}
	}

	if cr.Spec.ForProvider.BranchProtectionRules != nil {
		protectedBranches, err := listProtectedBranches(ctx, c.github, cr.Spec.ForProvider.Org, name)
		if err != nil {
			return managed.ExternalCreation{}, err
		}
		// getBPRMapFromCr() provides defaults for optional *bool fields
		rulesMap := getBPRMapFromCr(cr.Spec.ForProvider.BranchProtectionRules)
		_, err = filterMissingBranchProtectionRules(ctx, c.github, cr.Spec.ForProvider.Org, name, rulesMap, protectedBranchSet(protectedBranches))
		if err != nil {
			return managed.ExternalCreation{}, err
		}
		for key := range rulesMap {
			// avoid "G601: Implicit memory aliasing in for loop"
			rule := rulesMap[key]
			// Status set here is lost: the reconciler re-reads the CR after Create.
			_, err = editProtectedBranch(ctx, &rule, c.github, cr.Spec.ForProvider.Org, name)
			if err != nil {
				return managed.ExternalCreation{}, err
			}
		}
	}
	if cr.Spec.ForProvider.RepositoryRules != nil {
		// An invalid spec returns no rulesets to create; Observe reports it through RulesetsPartial.
		rulesMap, _ := getRepositoryRulesMapFromCr(*cr.Spec.ForProvider.RepositoryRules)
		for key := range rulesMap {
			// avoid "G601: Implicit memory aliasing in for loop"
			rule := rulesMap[key]
			ruleset, err := crRepoRulesToRulesConfig(rule)
			if err != nil {
				return managed.ExternalCreation{}, err
			}
			_, _, err = c.github.Rulesets.CreateRuleset(ctx, cr.Spec.ForProvider.Org, name, *ruleset)
			if err != nil {
				return managed.ExternalCreation{}, err
			}
		}

	}

	// Set topics if specified
	if len(cr.Spec.ForProvider.Topics) > 0 {
		_, _, err = c.github.Repositories.ReplaceAllTopics(ctx, cr.Spec.ForProvider.Org, name, cr.Spec.ForProvider.Topics)
		if err != nil {
			return managed.ExternalCreation{}, err
		}
	}

	cr.SetConditions(xpv2.Available())

	return managed.ExternalCreation{}, nil
}

// Condition surfaced when one or more declared collaborators can't be brought to
// their declared state right now — currently because they have an outstanding
// (unaccepted) repository invitation. Kept quiet like BranchProtectionPartial:
// SetConditions only writes when (Status, Reason, Message) actually change.
const (
	typeCollaboratorPartial       xpv2.ConditionType   = "CollaboratorPartial"
	reasonPendingInvitation       xpv2.ConditionReason = "PendingInvitation"
	reasonRoleEnforcedByOrg       xpv2.ConditionReason = "RoleEnforcedByOrg"
	reasonAllCollaboratorsPresent xpv2.ConditionReason = "AllCollaboratorsPresent"
)

// setCollaboratorPartialCondition reports declared collaborators the controller can't
// bring to their declared state: those awaiting invitation acceptance, and org owners
// whose declared role GitHub overrides with admin. Keeps the skips visible on the CR
// instead of looping silently.
func setCollaboratorPartialCondition(cr *v1alpha1.Repository, pendingInvite, roleEnforced []string) {
	c := xpv2.Condition{Type: typeCollaboratorPartial, LastTransitionTime: metav1.Now()}
	if len(pendingInvite) == 0 && len(roleEnforced) == 0 {
		c.Status = corev1.ConditionFalse
		c.Reason = reasonAllCollaboratorsPresent
		cr.SetConditions(c)
		return
	}
	c.Status = corev1.ConditionTrue
	if len(pendingInvite) > 0 {
		c.Reason = reasonPendingInvitation
	} else {
		c.Reason = reasonRoleEnforcedByOrg
	}
	var parts []string
	if len(pendingInvite) > 0 {
		sort.Strings(pendingInvite)
		parts = append(parts, "awaiting invitation acceptance: "+strings.Join(pendingInvite, ", "))
	}
	if len(roleEnforced) > 0 {
		sort.Strings(roleEnforced)
		parts = append(parts, "declared role overridden by GitHub org-admin enforcement: "+strings.Join(roleEnforced, ", "))
	}
	c.Message = strings.Join(parts, "; ")
	cr.SetConditions(c)
}

// collaboratorCategorization buckets the union of declared and actual direct collaborators.
type collaboratorCategorization struct {
	observed      map[string]string // direct collaborators GitHub lists, with their roles
	toRemove      map[string]string // direct collaborators absent from the CR
	toUpsert      map[string]string // declared collaborators to add or change role
	pendingInvite []string          // declared collaborators with an unaccepted invitation
	roleEnforced  []string          // org owners whose declared (lower) role GitHub overrides with admin
}

func (cc *collaboratorCategorization) hasDrift() bool {
	return len(cc.toRemove) > 0 || len(cc.toUpsert) > 0
}

// categorizeCollaborators classifies collaborators for Observe and Update.
// ListCollaborators(direct) returns only accepted collaborators, so a declared user
// with an outstanding invitation never appears there; recognizing the pending
// invitation keeps the controller from re-inviting them on every reconcile.
func categorizeCollaborators(ctx context.Context, gh *ghclient.Client, org, repo string, crUsers []v1alpha1.RepositoryUser) (*collaboratorCategorization, error) {
	crM := getUserPermissionMapFromCr(crUsers)
	ghM, err := getRepoUsersWithPermissions(ctx, gh, org, repo)
	if err != nil {
		return nil, err
	}

	cc := &collaboratorCategorization{
		observed: ghM,
		toRemove: make(map[string]string),
		toUpsert: make(map[string]string),
	}

	for user, ghRole := range ghM {
		crRole, inCR := crM[user]
		if !inCR {
			cc.toRemove[user] = ghRole
			continue
		}
		if crRole == ghRole {
			continue
		}
		// Role mismatch. GitHub force-keeps admin for org owners on every repo, so a
		// lower declared role for one can't be applied; only (GH=admin, CR<admin) can
		// be enforced, so the org-admin probe runs only in that shape.
		if ghRole == orgRoleAdmin && crRole != orgRoleAdmin {
			enforced, err := isOrgAdmin(ctx, gh, org, user)
			if err != nil {
				return nil, err
			}
			if enforced {
				cc.roleEnforced = append(cc.roleEnforced, user)
				continue
			}
		}
		cc.toUpsert[user] = crRole
	}

	// Pending invitations only matter for declared users who aren't active
	// collaborators; fetch them lazily so steady state costs no extra call.
	var pending map[string]bool
	for user, crRole := range crM {
		if _, ok := ghM[user]; ok {
			continue
		}
		if pending == nil {
			logins, err := getPendingRepoInviteeLogins(ctx, gh, org, repo)
			if err != nil {
				return nil, err
			}
			pending = make(map[string]bool, len(logins))
			for _, l := range logins {
				pending[l] = true
			}
		}
		if pending[user] {
			cc.pendingInvite = append(cc.pendingInvite, user)
			continue
		}
		cc.toUpsert[user] = crRole
	}

	return cc, nil
}

const orgRoleAdmin = "admin"

// isOrgAdmin reports whether the user is an organization owner (org-level admin).
// Org owners hold admin on every repo, so GitHub ignores a lower declared role.
// 404 (not a member) and a missing/other role are treated as not-admin.
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

// getPendingRepoInviteeLogins returns the lowercased logins of users with an
// outstanding (unaccepted) repository invitation. Email-only invitations are skipped.
func getPendingRepoInviteeLogins(ctx context.Context, gh *ghclient.Client, org, repo string) ([]string, error) {
	opt := &github.ListOptions{PerPage: 100}
	var logins []string
	for {
		invitations, resp, err := gh.Repositories.ListInvitations(ctx, org, repo, opt)
		if err != nil {
			return nil, err
		}
		for _, inv := range invitations {
			if inv == nil || inv.Invitee == nil || inv.Invitee.Login == nil {
				continue
			}
			logins = append(logins, strings.ToLower(*inv.Invitee.Login))
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opt.Page = resp.NextPage
	}
	return logins, nil
}

func updateRepoUsers(ctx context.Context, cr *v1alpha1.Repository, gh *ghclient.Client, repoName string) error {
	collaborators, err := categorizeCollaborators(ctx, gh, cr.Spec.ForProvider.Org, repoName, cr.Spec.ForProvider.Permissions.Users)
	if err != nil {
		return err
	}

	for userName := range collaborators.toRemove {
		if _, err := gh.Repositories.RemoveCollaborator(ctx, cr.Spec.ForProvider.Org, repoName, userName); err != nil {
			return err
		}
	}

	// Declared collaborators with an outstanding invitation are in flight and stay in
	// pendingInvite, not toUpsert, so they are not re-invited every reconcile.
	for userName, role := range collaborators.toUpsert {
		opt := &github.RepositoryAddCollaboratorOptions{Permission: role}
		if _, _, err := gh.Repositories.AddCollaborator(ctx, cr.Spec.ForProvider.Org, repoName, userName, opt); err != nil {
			return err
		}
	}

	return nil
}

// removeArchivedRepoUsers removes collaborators absent from the CR. While archived,
// GitHub permits collaborator removals but rejects additions and role changes with
// 403, so only removals are applied; skipped additions are surfaced by Observe via
// the ArchivedConfigFrozen condition.
func removeArchivedRepoUsers(ctx context.Context, cr *v1alpha1.Repository, gh *ghclient.Client, repoName string) error {
	crUsers := getUserPermissionMapFromCr(cr.Spec.ForProvider.Permissions.Users)
	ghUsers, err := getRepoUsersWithPermissions(ctx, gh, cr.Spec.ForProvider.Org, repoName)
	if err != nil {
		return err
	}
	toDelete, _, _ := util.DiffPermissions(ghUsers, crUsers)
	for userName := range toDelete {
		if _, err := gh.Repositories.RemoveCollaborator(ctx, cr.Spec.ForProvider.Org, repoName, userName); err != nil {
			return err
		}
	}
	return nil
}

func updateRepoTeams(ctx context.Context, cr *v1alpha1.Repository, gh *ghclient.Client, repoName string) error {
	crTToPermission := getTeamPermissionMapFromCr(cr.Spec.ForProvider.Permissions.Teams)
	ghTToPermission, err := getRepoTeamsWithPermissions(ctx, gh, cr.Spec.ForProvider.Org, repoName)
	if err != nil {
		return err
	}

	toDelete, toAdd, toUpdate := util.DiffPermissions(ghTToPermission, crTToPermission)

	for teamSlug := range toDelete {
		_, err := gh.Teams.RemoveTeamRepoBySlug(ctx, cr.Spec.ForProvider.Org, teamSlug, cr.Spec.ForProvider.Org, repoName)
		if err != nil {
			return err
		}
	}

	for teamSlug, role := range util.MergeMaps(toAdd, toUpdate) {
		opt := &github.TeamAddTeamRepoOptions{Permission: role}
		_, err := gh.Teams.AddTeamRepoBySlug(ctx, cr.Spec.ForProvider.Org, teamSlug, cr.Spec.ForProvider.Org, repoName, opt)
		if err != nil {
			return err
		}
	}

	return nil
}

// crRepoHookToHookConfig converts a RepositoryWebhook object to a *github.Hook object and returns it.
func crRepoHookToHookConfig(hook v1alpha1.RepositoryWebhook) *github.Hook {
	insecureSsl := "0"
	if hook.InsecureSSL != nil && *hook.InsecureSSL {
		insecureSsl = "1"
	}
	return &github.Hook{
		Config: &github.HookConfig{
			ContentType: &hook.ContentType,
			InsecureSSL: &insecureSsl,
			URL:         &hook.URL,
			Secret:      hook.Secret,
		},
		Events: hook.Events,
		Active: hook.Active,
	}
}

//nolint:gocyclo
func updateRepoWebhooks(c *external, ctx context.Context, cr *v1alpha1.Repository, gh *ghclient.Client, repoName string) error {
	ghRepoWebhooks, err := getRepoWebhooks(ctx, gh, cr.Spec.ForProvider.Org, repoName)
	if err != nil {
		return err
	}
	crWToConfig, err := c.getRepoWebhooksMapFromCr(ctx, cr.Spec.ForProvider.Webhooks)
	if err != nil {
		return err
	}
	ghWToConfig, err := c.getRepoWebhooksWithConfig(ctx, ghRepoWebhooks, cr)
	if err != nil {
		return err
	}

	toDelete, toAdd, toUpdate := util.DiffRepoWebhooks(ghWToConfig, crWToConfig)

	for _, hook := range toDelete {
		id, err := getRepoWebhookId(ghRepoWebhooks, hook.URL)
		if err != nil {
			return err
		}
		_, err = gh.Repositories.DeleteHook(ctx, cr.Spec.ForProvider.Org, repoName, *id)
		if err != nil {
			return err
		}
		if hook.Secret != nil {
			err = c.deleteConnectionSecretEntry(ctx, cr, util.GenerateSHA1Hash(hook.URL))
			if err != nil {
				return err
			}
		}
	}

	for _, hook := range toAdd {
		hookConfig := crRepoHookToHookConfig(hook)
		_, _, err = gh.Repositories.CreateHook(ctx, cr.Spec.ForProvider.Org, repoName, hookConfig)
		if err != nil {
			return err
		}
		if hookConfig.Config.Secret != nil {
			err = c.updateConnectionSecretEntry(ctx, cr, util.GenerateSHA1Hash(hook.URL), webhookSecretState{
				WebhookUrl:    *hookConfig.Config.URL,
				WebhookSecret: *hookConfig.Config.Secret,
			})
			if err != nil {
				return err
			}
		}
	}

	for _, hook := range toUpdate {
		id, err := getRepoWebhookId(ghRepoWebhooks, hook.URL)
		if err != nil {
			return err
		}
		hookConfig := crRepoHookToHookConfig(hook)
		_, _, err = gh.Repositories.EditHook(ctx, cr.Spec.ForProvider.Org, repoName, *id, hookConfig)
		if err != nil {
			return err
		}

		// Clear connection secret entry first, if it exists
		err = c.deleteConnectionSecretEntry(ctx, cr, util.GenerateSHA1Hash(*hookConfig.Config.URL))
		if err != nil {
			return err
		}

		// Add updated connection secret entry, if needed
		if hookConfig.Config.Secret != nil {
			err = c.updateConnectionSecretEntry(ctx, cr, util.GenerateSHA1Hash(hook.URL), webhookSecretState{
				WebhookUrl:    *hookConfig.Config.URL,
				WebhookSecret: *hookConfig.Config.Secret,
			})
			if err != nil {
				return err
			}
		}
	}

	return nil
}

// editProtectedBranch updates the branch protection settings for a given GitHub repository
// based on a provided BranchProtectionRule. It returns the items GitHub's echo left out,
// or an error if the update operation fails.
//
//nolint:gocyclo
func editProtectedBranch(ctx context.Context, rule *v1alpha1.BranchProtectionRule, gh *ghclient.Client, owner, repoName string) ([]string, error) {
	protectionRequest := &github.ProtectionRequest{
		EnforceAdmins:                  rule.EnforceAdmins,
		RequireLinearHistory:           rule.RequireLinearHistory,
		AllowForcePushes:               rule.AllowForcePushes,
		AllowDeletions:                 rule.AllowDeletions,
		RequiredConversationResolution: rule.RequiredConversationResolution,
		LockBranch:                     rule.LockBranch,
		AllowForkSyncing:               rule.AllowForkSyncing,
	}

	if rule.RequiredStatusChecks != nil {
		// checks stays nil when there are no checks so the request body is unchanged.
		var checks []*github.RequiredStatusCheck
		if n := len(rule.RequiredStatusChecks.Checks); n > 0 {
			checks = make([]*github.RequiredStatusCheck, 0, n)
		}
		for _, check := range rule.RequiredStatusChecks.Checks {
			// if nil, allow any app to set the status of a check
			appId := pointer.Deref(check.AppID, -1)
			checks = append(checks, &github.RequiredStatusCheck{
				Context: check.Context,
				AppID:   &appId,
			})
		}
		protectionRequest.RequiredStatusChecks = &github.RequiredStatusChecks{
			Strict: rule.RequiredStatusChecks.Strict,
			Checks: &checks,
		}
	}

	if rule.RequiredPullRequestReviews != nil {
		emptySlice := make([]string, 0)
		protectionRequest.RequiredPullRequestReviews = &github.PullRequestReviewsEnforcementRequest{
			// Avoid unmanaged bypass allowances when they're not set in the CR
			BypassPullRequestAllowancesRequest: &github.BypassPullRequestAllowancesRequest{
				Users: emptySlice, Teams: emptySlice, Apps: emptySlice,
			},
			// Avoid unmanaged dismissal restrictions when they're not set in the CR
			DismissalRestrictionsRequest: &github.DismissalRestrictionsRequest{Users: nil, Teams: nil, Apps: nil},
			DismissStaleReviews:          rule.RequiredPullRequestReviews.DismissStaleReviews,
			RequireCodeOwnerReviews:      rule.RequiredPullRequestReviews.RequireCodeOwnerReviews,
			RequiredApprovingReviewCount: rule.RequiredPullRequestReviews.RequiredApprovingReviewCount,
			RequireLastPushApproval:      rule.RequiredPullRequestReviews.RequireLastPushApproval,
		}
		if rule.RequiredPullRequestReviews.BypassPullRequestAllowances != nil {
			protectionRequest.RequiredPullRequestReviews.BypassPullRequestAllowancesRequest = &github.BypassPullRequestAllowancesRequest{
				Users: util.DefaultToStringSlice(rule.RequiredPullRequestReviews.BypassPullRequestAllowances.Users),
				Teams: util.DefaultToStringSlice(rule.RequiredPullRequestReviews.BypassPullRequestAllowances.Teams),
				Apps:  util.DefaultToStringSlice(rule.RequiredPullRequestReviews.BypassPullRequestAllowances.Apps),
			}
		}
		if rule.RequiredPullRequestReviews.DismissalRestrictions != nil {
			protectionRequest.RequiredPullRequestReviews.DismissalRestrictionsRequest = &github.DismissalRestrictionsRequest{
				Users: rule.RequiredPullRequestReviews.DismissalRestrictions.Users,
				Teams: rule.RequiredPullRequestReviews.DismissalRestrictions.Teams,
				Apps:  rule.RequiredPullRequestReviews.DismissalRestrictions.Apps,
			}
		}
	}

	if rule.BranchProtectionRestrictions != nil {
		protectionRequest.BlockCreations = rule.BranchProtectionRestrictions.BlockCreations
		protectionRequest.Restrictions = &github.BranchRestrictionsRequest{
			Users: util.DefaultToStringSlice(rule.BranchProtectionRestrictions.Users),
			Teams: util.DefaultToStringSlice(rule.BranchProtectionRestrictions.Teams),
			Apps:  util.DefaultToStringSlice(rule.BranchProtectionRestrictions.Apps),
		}
	}

	protection, _, err := gh.Repositories.UpdateBranchProtection(ctx, owner, repoName, rule.Branch, protectionRequest)
	if err != nil {
		return nil, err
	}

	err = handleBranchProtectionSignature(ctx, gh, owner, repoName, rule)
	if err != nil {
		return nil, err
	}

	return unappliedItems(*rule, protectionToRule(rule.Branch, protection)), nil
}

// updateProtectedBranches synchronizes the branch protection rules of a GitHub repository
// to match with those detailed in the repository resource object.
// It performs necessary additions, updates, or deletions based on the difference between
// the actual state on GitHub and the desired state in the resource object.
func updateProtectedBranches(ctx context.Context, cr *v1alpha1.Repository, gh *ghclient.Client, repoName string) error {
	protectedBranches, err := listProtectedBranches(ctx, gh, cr.Spec.ForProvider.Org, repoName)
	if err != nil {
		return err
	}
	crBPRToConfig := getBPRMapFromCr(cr.Spec.ForProvider.BranchProtectionRules)
	_, err = filterMissingBranchProtectionRules(ctx, gh, cr.Spec.ForProvider.Org, repoName, crBPRToConfig, protectedBranchSet(protectedBranches))
	if err != nil {
		return err
	}
	ghBPRToConfig, err := getBPRWithConfig(ctx, gh, cr.Spec.ForProvider.Org, repoName, protectedBranches)
	if err != nil {
		return err
	}

	toDelete, toAdd, toUpdate := util.DiffProtectedBranches(ghBPRToConfig, crBPRToConfig)

	for branchName := range toDelete {
		_, err = gh.Repositories.RemoveBranchProtection(ctx, cr.Spec.ForProvider.Org, repoName, branchName)
		if err != nil {
			return err
		}
	}

	for key := range toAdd {
		// avoid "G601: Implicit memory aliasing in for loop"
		config := toAdd[key]
		items, err := editProtectedBranch(ctx, &config, gh, cr.Spec.ForProvider.Org, repoName)
		if err != nil {
			return err
		}
		setUnappliedBranchProtection(cr, config, items)
	}

	for key := range toUpdate {
		// avoid "G601: Implicit memory aliasing in for loop"
		config := toUpdate[key]
		items, err := editProtectedBranch(ctx, &config, gh, cr.Spec.ForProvider.Org, repoName)
		if err != nil {
			return err
		}
		setUnappliedBranchProtection(cr, config, items)
	}

	return nil
}

// handleBranchProtectionSignature manages the requirement of signed commits for protected branches
// depending on the configuration. If RequireSignedCommits is set to true, it enforces signed commits,
// making them mandatory for all contributors. If it's false, signing commits is optional.
// It returns an error if any of the GitHub API calls fail.
func handleBranchProtectionSignature(ctx context.Context, gh *ghclient.Client, owner, repoName string, protectionRule *v1alpha1.BranchProtectionRule) error {
	if protectionRule.RequireSignedCommits != nil && *protectionRule.RequireSignedCommits {
		_, _, err := gh.Repositories.RequireSignaturesOnProtectedBranch(ctx, owner, repoName, protectionRule.Branch)
		if err != nil {
			return err
		}
	} else {
		_, err := gh.Repositories.OptionalSignaturesOnProtectedBranch(ctx, owner, repoName, protectionRule.Branch)
		if err != nil {
			return err
		}
	}
	return nil
}

// getRepositoryRules retrieves the repository's own rulesets.
// It uses pagination to handle large numbers of rules, fetching 100 rules per API call.
// Inherited rulesets belong to the organization or enterprise and are managed there.
func getRepositoryRules(ctx context.Context, gh *ghclient.Client, org, repo string) ([]*rulesets.Ruleset, error) {
	opt := &github.RepositoryListRulesetsOptions{
		// GitHub lists inherited rulesets by default.
		IncludesParents: github.Ptr(false),
		ListOptions:     github.ListOptions{PerPage: 100},
	}
	var allRules []*rulesets.Ruleset

	for {
		rules, resp, err := gh.Rulesets.GetAllRulesets(ctx, org, repo, opt)
		if err != nil {
			return nil, err
		}

		for _, rule := range rules {
			if rule.SourceType != nil && *rule.SourceType != rulesetSourceRepository {
				continue
			}
			allRules = append(allRules, rule)
		}

		if resp.NextPage == 0 {
			break
		}
		opt.Page = resp.NextPage
	}

	return allRules, nil
}

// getRepositoryRulesMapFromCr generates a map from the RepositoryRules slice
// in the Crossplane resource, each ruleset in the form normalizeRuleset returns.
func getRepositoryRulesMapFromCr(rules []v1alpha1.RepositoryRuleset) (map[string]v1alpha1.RepositoryRuleset, error) {
	crRulesToConfig := make(map[string]v1alpha1.RepositoryRuleset, len(rules))

	for i := range rules {
		rule, err := normalizeRuleset(rules[i])
		if err != nil {
			return nil, err
		}
		crRulesToConfig[rule.Name] = rule
	}

	return crRulesToConfig, nil
}

// normalizeRuleset returns a copy of the ruleset in the form that is compared and sent:
// unset fields hold GitHub's defaults and lists GitHub treats as sets are sorted. It
// returns the same ruleset for a ruleset it has already normalised.
//
//nolint:gocyclo
func normalizeRuleset(rule v1alpha1.RepositoryRuleset) (v1alpha1.RepositoryRuleset, error) {
	// Use a copy to avoid changing passed []v1alpha1.RepositoryRules
	// This prevents the controller from changing the spec of the live CR
	// It can also prevent infinite reconciliation loops when managing the resources with ArgoCD
	rCopy := rule.DeepCopy()

	// handle optional fields
	rCopy.Target = util.StringDerefToPointer(rCopy.Target, "branch")
	rCopy.Enforcement = util.StringDerefToPointer(rCopy.Enforcement, "active")

	rCopy.Conditions = rulesetConditions(*rCopy.Target, rCopy.Conditions)
	rCopy.BypassActors = rulesetBypassActors(rCopy.BypassActors)
	if err := duplicateBypassActor(rCopy.Name, rCopy.BypassActors); err != nil {
		return v1alpha1.RepositoryRuleset{}, err
	}
	if err := duplicateRefPattern(rCopy.Name, rCopy.Conditions); err != nil {
		return v1alpha1.RepositoryRuleset{}, err
	}

	// Unset rules mean every rule is off.
	if rCopy.Rules == nil {
		rCopy.Rules = &v1alpha1.Rules{}
	}
	rRules := rCopy.Rules
	rRules.RequiredSignatures = util.BoolDerefToPointer(rRules.RequiredSignatures, false)
	rRules.NonFastForward = util.BoolDerefToPointer(rRules.NonFastForward, false)
	rRules.Creation = util.BoolDerefToPointer(rRules.Creation, false)
	rRules.Deletion = util.BoolDerefToPointer(rRules.Deletion, false)
	rRules.RequiredLinearHistory = util.BoolDerefToPointer(rRules.RequiredLinearHistory, false)
	rRules.Update = util.BoolDerefToPointer(rRules.Update, false)
	rRules.LicenseComplianceScanning = util.BoolDerefToPointer(rRules.LicenseComplianceScanning, false)
	// Fetch and merge is a parameter of the update rule, so it is set only with that rule.
	if *rRules.Update {
		rRules.UpdateAllowsFetchAndMerge = util.BoolDerefToPointer(rRules.UpdateAllowsFetchAndMerge, false)
	} else {
		rRules.UpdateAllowsFetchAndMerge = nil
	}
	if r := rRules.RequireSecretScanningAlertResolution; r != nil {
		r.SecretTypes = secretTypeSet(r.SecretTypes)
	}

	if rRules.RequiredDeployments != nil {
		rRules.RequiredDeployments.Environments = util.SortAndReturn(util.DefaultToStringSlice(rRules.RequiredDeployments.Environments))
	}
	if rRules.PullRequest != nil {
		pullRequestDefaults(rRules.PullRequest)
	}
	if rRules.RequiredStatusChecks != nil {
		copyOfStatusChecks := make([]*v1alpha1.RulesRequiredStatusChecksParameters, len(rRules.RequiredStatusChecks.RequiredStatusChecks))
		copy(copyOfStatusChecks, rRules.RequiredStatusChecks.RequiredStatusChecks)
		util.SortRulesRequiredStatusChecks(copyOfStatusChecks)
		rRules.RequiredStatusChecks.RequiredStatusChecks = copyOfStatusChecks
		rRules.RequiredStatusChecks.StrictRequiredStatusChecksPolicy = util.BoolDerefToPointer(rRules.RequiredStatusChecks.StrictRequiredStatusChecksPolicy, false)
		rRules.RequiredStatusChecks.DoNotEnforceOnCreate = util.BoolDerefToPointer(rRules.RequiredStatusChecks.DoNotEnforceOnCreate, false)
	}
	for _, p := range []*v1alpha1.RulesPattern{rRules.CommitMessagePattern, rRules.CommitAuthorEmailPattern, rRules.CommitterEmailPattern, rRules.BranchNamePattern, rRules.TagNamePattern} {
		if p != nil {
			p.Name = util.StringDerefToPointer(p.Name, "")
			p.Negate = util.BoolDerefToPointer(p.Negate, false)
		}
	}
	if rRules.CodeCoverage != nil {
		rRules.CodeCoverage.MinimumCoverage = canonicalDecimal(rRules.CodeCoverage.MinimumCoverage)
		rRules.CodeCoverage.MaxCoverageDrop = canonicalDecimal(rRules.CodeCoverage.MaxCoverageDrop)
	}
	if rRules.CodeScanning != nil {
		util.SortRulesCodeScanningTools(rRules.CodeScanning.CodeScanningTools)
	}
	if rRules.CopilotCodeReview != nil {
		rRules.CopilotCodeReview.ReviewOnPush = util.BoolDerefToPointer(rRules.CopilotCodeReview.ReviewOnPush, false)
		rRules.CopilotCodeReview.ReviewDraftPullRequests = util.BoolDerefToPointer(rRules.CopilotCodeReview.ReviewDraftPullRequests, false)
	}
	if rRules.FileExtensionRestriction != nil {
		rRules.FileExtensionRestriction.RestrictedFileExtensions = util.SortAndReturn(rRules.FileExtensionRestriction.RestrictedFileExtensions)
	}
	if rRules.FilePathRestriction != nil {
		rRules.FilePathRestriction.RestrictedFilePaths = util.SortAndReturn(rRules.FilePathRestriction.RestrictedFilePaths)
		rRules.FilePathRestriction.IgnoredFilePaths = util.SortAndReturn(util.DefaultToStringSlice(rRules.FilePathRestriction.IgnoredFilePaths))
	}
	if rRules.MaxFileSize != nil {
		rRules.MaxFileSize.IgnoredFilePaths = util.SortAndReturn(util.DefaultToStringSlice(rRules.MaxFileSize.IgnoredFilePaths))
	}
	return *rCopy, nil
}

// getRepositoryRulesWithConfig returns the repository's rulesets in CR form, keyed by
// name. It fetches the rulesets the CR names; the others are listed by name only,
// because they are going to be deleted. A fetched ruleset holding rule types the
// provider does not model is left out of the result, and its name is in
// report.skipped, so the callers drop it from their CR map as well and compare and
// diff leave it as it is. The report names those rulesets and the rules of the other
// fetched rulesets that hold parameters the provider does not manage, and lists in
// extraApprovalReturned the rulesets whose desired
// requireExtraApprovalForUnattributedChanges is GitHub's default; the callers apply
// both lists to their CR map with applyRulesetReport.
//
//nolint:gocyclo
func getRepositoryRulesWithConfig(ctx context.Context, gh *ghclient.Client, owner, repo string, ghRulesets []*rulesets.Ruleset, crRulesets map[string]v1alpha1.RepositoryRuleset) (map[string]v1alpha1.RepositoryRuleset, rulesetReport, error) {
	rulesToConfig := make(map[string]v1alpha1.RepositoryRuleset, len(ghRulesets))
	report := rulesetReport{observed: make(map[string]v1alpha1.RepositoryRuleset, len(ghRulesets))}

	for _, rule := range ghRulesets {
		if _, inCR := crRulesets[rule.Name]; !inCR {
			rulesToConfig[rule.Name] = v1alpha1.RepositoryRuleset{Name: rule.Name}
			report.observed[rule.Name] = v1alpha1.RepositoryRuleset{Name: rule.Name}
			continue
		}
		rRuleset, _, err := gh.Rulesets.GetRuleset(ctx, owner, repo, *rule.ID, false)
		if err != nil {
			return nil, rulesetReport{}, err
		}
		ruleset, convErr := rulesetFromGitHub(rule, rRuleset)
		if convErr == nil {
			report.observed[rule.Name] = *ruleset.DeepCopy()
		}
		if types := unmanagedRuleTypes(rRuleset.Rules); len(types) > 0 {
			report.unmanagedTypes = append(report.unmanagedTypes, fmt.Sprintf("ruleset %s has rule types this provider does not manage (%s); it is left as it is until they are removed on GitHub or modelled by the provider", rule.Name, strings.Join(types, ", ")))
			report.skipped = append(report.skipped, rule.Name)
			continue
		}
		for _, r := range rRuleset.Rules {
			if keys := rulesets.UnmanagedParameters(r); len(keys) > 0 {
				report.unmanagedParameters = append(report.unmanagedParameters, unmanagedParametersSentence(rule.Name, r.Type, keys))
			}
		}
		if convErr != nil {
			return nil, rulesetReport{}, convErr
		}
		withoutUndeclared(&ruleset, crRulesets[rule.Name])
		if c, g := crRulesets[rule.Name].Rules, ruleset.Rules.PullRequest; c != nil && c.PullRequest != nil && c.PullRequest.RequireExtraApprovalForUnattributedChanges == nil && g != nil && g.RequireExtraApprovalForUnattributedChanges != nil {
			report.extraApprovalReturned = append(report.extraApprovalReturned, rule.Name)
		}
		rulesToConfig[rule.Name] = ruleset
	}

	slices.Sort(report.unmanagedTypes)
	slices.Sort(report.unmanagedParameters)
	slices.Sort(report.skipped)
	slices.Sort(report.extraApprovalReturned)
	return rulesToConfig, report, nil

}

// rulesetFromGitHub converts a ruleset GitHub holds into its CR form, with the rule
// types and parameters the provider models. rule is the entry of the list and
// rRuleset the ruleset fetched by its ID.
//
//nolint:gocyclo
func rulesetFromGitHub(rule, rRuleset *rulesets.Ruleset) (v1alpha1.RepositoryRuleset, error) {
	target := pointer.Deref(rule.Target, "")
	ruleset := v1alpha1.RepositoryRuleset{
		Target:      pointer.To(target),
		Enforcement: &rule.Enforcement,
		Name:        rule.Name,
		Rules: &v1alpha1.Rules{
			Creation:                  pointer.To(false),
			Update:                    pointer.To(false),
			Deletion:                  pointer.To(false),
			RequiredLinearHistory:     pointer.To(false),
			RequiredDeployments:       nil,
			RequiredSignatures:        pointer.To(false),
			NonFastForward:            pointer.To(false),
			LicenseComplianceScanning: pointer.To(false),
			PullRequest:               nil,
			RequiredStatusChecks:      nil,
		},
	}

	var conditions *v1alpha1.RulesetConditions
	if c := rRuleset.Conditions; c != nil && c.RefName != nil {
		conditions = &v1alpha1.RulesetConditions{RefName: &v1alpha1.RulesetRefName{Include: c.RefName.Include, Exclude: c.RefName.Exclude}}
	}
	ruleset.Conditions = rulesetConditions(target, conditions)

	actors := make([]*v1alpha1.RulesetByPassActors, len(rRuleset.BypassActors))
	for i, actor := range rRuleset.BypassActors {
		actors[i] = &v1alpha1.RulesetByPassActors{
			ActorType:  actor.ActorType,
			ActorID:    actor.ActorID,
			BypassMode: actor.BypassMode,
		}
	}
	ruleset.BypassActors = rulesetBypassActors(actors)

	if rRuleset.Rules != nil {
		rules, err := rulesets.Decode(rRuleset.Rules)
		if err != nil {
			return v1alpha1.RepositoryRuleset{}, err
		}
		if rules.Creation != nil {
			ruleset.Rules.Creation = pointer.To(true)
		}
		if rules.Deletion != nil {
			ruleset.Rules.Deletion = pointer.To(true)
		}
		if rules.RequiredLinearHistory != nil {
			ruleset.Rules.RequiredLinearHistory = pointer.To(true)
		}
		if rules.RequiredSignatures != nil {
			ruleset.Rules.RequiredSignatures = pointer.To(true)
		}
		if rules.NonFastForward != nil {
			ruleset.Rules.NonFastForward = pointer.To(true)
		}
		if rules.Update != nil {
			ruleset.Rules.Update = pointer.To(true)
			// GitHub returns this on forks only. Elsewhere it takes the CR's value.
			ruleset.Rules.UpdateAllowsFetchAndMerge = rules.Update.UpdateAllowsFetchAndMerge
		}
		if rules.LicenseComplianceScanning != nil {
			ruleset.Rules.LicenseComplianceScanning = pointer.To(true)
		}
		if params := rules.RequireSecretScanningAlertResolution; params != nil {
			var types []v1alpha1.RulesSecretType
			for _, t := range params.SecretTypes {
				types = append(types, v1alpha1.RulesSecretType(t))
			}
			ruleset.Rules.RequireSecretScanningAlertResolution = &v1alpha1.RulesSecretScanningAlertResolution{SecretTypes: secretTypeSet(types)}
		}
		if params := rules.PullRequest; params != nil {
			if ruleset.Rules.PullRequest, err = pullRequestFromGitHub(params); err != nil {
				return v1alpha1.RepositoryRuleset{}, errors.Wrap(err, "rule pull_request")
			}
		}
		if params := rules.RequiredDeployments; params != nil {
			ruleset.Rules.RequiredDeployments = &v1alpha1.RulesRequiredDeployments{
				Environments: util.SortAndReturn(util.DefaultToStringSlice(params.RequiredDeploymentEnvironments)),
			}
		}
		if params := rules.RequiredStatusChecks; params != nil {
			requiredStatusChecksParameters := make([]*v1alpha1.RulesRequiredStatusChecksParameters, len(params.RequiredStatusChecks))
			for i, statusCheck := range params.RequiredStatusChecks {
				requiredStatusChecksParameters[i] = &v1alpha1.RulesRequiredStatusChecksParameters{
					Context:       statusCheck.Context,
					IntegrationID: statusCheck.IntegrationID,
				}
			}
			util.SortRulesRequiredStatusChecks(requiredStatusChecksParameters)

			ruleset.Rules.RequiredStatusChecks = &v1alpha1.RulesRequiredStatusChecks{
				DoNotEnforceOnCreate:             pointer.To(params.DoNotEnforceOnCreate),
				StrictRequiredStatusChecksPolicy: pointer.To(params.StrictRequiredStatusChecksPolicy),
				RequiredStatusChecks:             requiredStatusChecksParameters,
			}
		}
		if params := rules.MergeQueue; params != nil {
			ruleset.Rules.MergeQueue = &v1alpha1.RulesMergeQueue{
				CheckResponseTimeoutMinutes:  params.CheckResponseTimeoutMinutes,
				GroupingStrategy:             params.GroupingStrategy,
				MaxEntriesToBuild:            params.MaxEntriesToBuild,
				MaxEntriesToMerge:            params.MaxEntriesToMerge,
				MergeMethod:                  params.MergeMethod,
				MinEntriesToMerge:            params.MinEntriesToMerge,
				MinEntriesToMergeWaitMinutes: params.MinEntriesToMergeWaitMinutes,
			}
		}
		ruleset.Rules.CommitMessagePattern = patternRuleFromGitHub(rules.CommitMessagePattern)
		ruleset.Rules.CommitAuthorEmailPattern = patternRuleFromGitHub(rules.CommitAuthorEmailPattern)
		ruleset.Rules.CommitterEmailPattern = patternRuleFromGitHub(rules.CommitterEmailPattern)
		ruleset.Rules.BranchNamePattern = patternRuleFromGitHub(rules.BranchNamePattern)
		ruleset.Rules.TagNamePattern = patternRuleFromGitHub(rules.TagNamePattern)
		if params := rules.CodeScanning; params != nil {
			tools := make([]*v1alpha1.RulesCodeScanningTool, len(params.CodeScanningTools))
			for i, tool := range params.CodeScanningTools {
				tools[i] = &v1alpha1.RulesCodeScanningTool{
					Tool:                    tool.Tool,
					AlertsThreshold:         tool.AlertsThreshold,
					SecurityAlertsThreshold: tool.SecurityAlertsThreshold,
				}
			}
			util.SortRulesCodeScanningTools(tools)
			ruleset.Rules.CodeScanning = &v1alpha1.RulesCodeScanning{CodeScanningTools: tools}
		}
		if params := rules.CodeQuality; params != nil {
			ruleset.Rules.CodeQuality = &v1alpha1.RulesCodeQuality{Severity: params.Severity}
		}
		if params := rules.CodeCoverage; params != nil {
			ruleset.Rules.CodeCoverage = &v1alpha1.RulesCodeCoverage{
				MinimumCoverage: decimalFromGitHub(params.MinimumCoverage),
				MaxCoverageDrop: decimalFromGitHub(params.MaxCoverageDrop),
			}
		}
		if params := rules.CopilotCodeReview; params != nil {
			ruleset.Rules.CopilotCodeReview = &v1alpha1.RulesCopilotCodeReview{
				ReviewOnPush:            pointer.To(params.ReviewOnPush),
				ReviewDraftPullRequests: pointer.To(params.ReviewDraftPullRequests),
			}
		}
		if params := rules.FileExtensionRestriction; params != nil {
			ruleset.Rules.FileExtensionRestriction = &v1alpha1.RulesFileExtensionRestriction{
				RestrictedFileExtensions: util.SortAndReturn(params.RestrictedFileExtensions),
			}
		}
		if params := rules.FilePathRestriction; params != nil {
			ruleset.Rules.FilePathRestriction = &v1alpha1.RulesFilePathRestriction{
				RestrictedFilePaths: util.SortAndReturn(params.RestrictedFilePaths),
				IgnoredFilePaths:    util.SortAndReturn(util.DefaultToStringSlice(params.IgnoredFilePaths)),
			}
		}
		if params := rules.MaxFilePathLength; params != nil {
			ruleset.Rules.MaxFilePathLength = &v1alpha1.RulesMaxFilePathLength{MaxFilePathLength: params.MaxFilePathLength}
		}
		if params := rules.MaxFileSize; params != nil {
			ruleset.Rules.MaxFileSize = &v1alpha1.RulesMaxFileSize{
				MaxFileSize:      params.MaxFileSize,
				IgnoredFilePaths: util.SortAndReturn(util.DefaultToStringSlice(params.IgnoredFilePaths)),
			}
		}
	}
	return ruleset, nil
}

// patternRuleFromGitHub converts a GitHub pattern rule into its CR form. A missing
// name becomes "" and a missing negate becomes false.
func patternRuleFromGitHub(params *rulesets.PatternRuleParameters) *v1alpha1.RulesPattern {
	if params == nil {
		return nil
	}
	return &v1alpha1.RulesPattern{
		Name:     util.StringDerefToPointer(params.Name, ""),
		Negate:   util.BoolDerefToPointer(params.Negate, false),
		Operator: params.Operator,
		Pattern:  params.Pattern,
	}
}

// unmanagedParametersSentence states that the next update of ruleset resets the
// parameters keys of its ruleType rule.
func unmanagedParametersSentence(ruleset, ruleType string, keys []string) string {
	if len(keys) == 1 {
		return fmt.Sprintf("ruleset %s: rule %s has parameter %s that this provider does not manage; GitHub resets it when the ruleset is next updated", ruleset, ruleType, keys[0])
	}
	return fmt.Sprintf("ruleset %s: rule %s has parameters %s that this provider does not manage; GitHub resets them when the ruleset is next updated", ruleset, ruleType, strings.Join(keys, ", "))
}

// unmanagedRuleTypes returns the sorted, unique types of the unmodelled rules in a
// ruleset. An update would delete these rules, so the caller leaves the ruleset as it is.
func unmanagedRuleTypes(rules []*rulesets.Rule) []string {
	var types []string
	for _, rule := range rules {
		if !rulesets.IsModelled(rule.Type) {
			types = append(types, rule.Type)
		}
	}
	slices.Sort(types)
	return slices.Compact(types)
}

// crRepoRulesToRulesConfig transforms a RepositoryRuleset object from the Crossplane resource
// into a Ruleset object that can be used with the GitHub API. It normalises the ruleset
// first, so it takes the ruleset as declared or as getRepositoryRulesMapFromCr returns it.
//
//nolint:gocyclo
func crRepoRulesToRulesConfig(rule v1alpha1.RepositoryRuleset) (*rulesets.Ruleset, error) {
	rule, err := normalizeRuleset(rule)
	if err != nil {
		return nil, err
	}
	// Bypass actors and rules are always sent, as [] when empty, because an update keeps
	// what GitHub holds for an omitted key.
	githubRuleset := &rulesets.Ruleset{
		Name:         rule.Name,
		Enforcement:  *rule.Enforcement,
		Target:       rule.Target,
		BypassActors: make([]*rulesets.BypassActor, len(rule.BypassActors)),
		Rules:        []*rulesets.Rule{},
	}

	for i, actor := range rule.BypassActors {
		githubRuleset.BypassActors[i] = &rulesets.BypassActor{
			ActorID:    actor.ActorID,
			ActorType:  actor.ActorType,
			BypassMode: actor.BypassMode,
		}
	}

	// A push ruleset is sent "conditions": {}, which clears a ref_name left from an
	// earlier target. GitHub rejects ref_name on push rulesets.
	if pointer.Deref(rule.Target, "") == rulesetTargetPush {
		githubRuleset.Conditions = &rulesets.Conditions{}
	} else if rule.Conditions != nil && rule.Conditions.RefName != nil {
		githubRuleset.Conditions = &rulesets.Conditions{
			RefName: &rulesets.RefName{
				Include: util.DefaultToStringSlice(rule.Conditions.RefName.Include),
				Exclude: util.DefaultToStringSlice(rule.Conditions.RefName.Exclude),
			},
		}
	}
	githubRules := rulesets.ModelledRules{}
	if rule.Rules.RequiredStatusChecks != nil {
		params := rulesets.RequiredStatusChecksRuleParameters{
			DoNotEnforceOnCreate:             *rule.Rules.RequiredStatusChecks.DoNotEnforceOnCreate,
			StrictRequiredStatusChecksPolicy: *rule.Rules.RequiredStatusChecks.StrictRequiredStatusChecksPolicy,
		}
		requiredStatusChecks := make([]*rulesets.StatusCheck, len(rule.Rules.RequiredStatusChecks.RequiredStatusChecks))
		for i, statusCheck := range rule.Rules.RequiredStatusChecks.RequiredStatusChecks {
			requiredStatusChecks[i] = &rulesets.StatusCheck{
				Context:       statusCheck.Context,
				IntegrationID: statusCheck.IntegrationID,
			}
		}
		params.RequiredStatusChecks = requiredStatusChecks
		githubRules.RequiredStatusChecks = &params
	}

	if *rule.Rules.Creation {
		githubRules.Creation = &rulesets.EmptyRuleParameters{}
	}

	if *rule.Rules.Deletion {
		githubRules.Deletion = &rulesets.EmptyRuleParameters{}
	}

	if *rule.Rules.RequiredLinearHistory {
		githubRules.RequiredLinearHistory = &rulesets.EmptyRuleParameters{}
	}

	if *rule.Rules.RequiredSignatures {
		githubRules.RequiredSignatures = &rulesets.EmptyRuleParameters{}
	}
	if *rule.Rules.NonFastForward {
		githubRules.NonFastForward = &rulesets.EmptyRuleParameters{}
	}
	if *rule.Rules.Update {
		githubRules.Update = &rulesets.UpdateRuleParameters{UpdateAllowsFetchAndMerge: rule.Rules.UpdateAllowsFetchAndMerge}
	}
	if *rule.Rules.LicenseComplianceScanning {
		githubRules.LicenseComplianceScanning = &rulesets.EmptyRuleParameters{}
	}
	if r := rule.Rules.RequireSecretScanningAlertResolution; r != nil {
		params := &rulesets.RequireSecretScanningAlertResolutionRuleParameters{SecretTypes: []string{}}
		for _, t := range r.SecretTypes {
			params.SecretTypes = append(params.SecretTypes, string(t))
		}
		githubRules.RequireSecretScanningAlertResolution = params
	}
	if rule.Rules.PullRequest != nil {
		githubRules.PullRequest = pullRequestToGitHub(rule.Rules.PullRequest)
	}
	if rule.Rules.RequiredDeployments != nil {
		githubRules.RequiredDeployments = &rulesets.RequiredDeploymentsRuleParameters{
			RequiredDeploymentEnvironments: rule.Rules.RequiredDeployments.Environments,
		}
	}
	if mq := rule.Rules.MergeQueue; mq != nil {
		githubRules.MergeQueue = &rulesets.MergeQueueRuleParameters{
			CheckResponseTimeoutMinutes:  mq.CheckResponseTimeoutMinutes,
			GroupingStrategy:             mq.GroupingStrategy,
			MaxEntriesToBuild:            mq.MaxEntriesToBuild,
			MaxEntriesToMerge:            mq.MaxEntriesToMerge,
			MergeMethod:                  mq.MergeMethod,
			MinEntriesToMerge:            mq.MinEntriesToMerge,
			MinEntriesToMergeWaitMinutes: mq.MinEntriesToMergeWaitMinutes,
		}
	}
	githubRules.CommitMessagePattern = patternRuleToGitHub(rule.Rules.CommitMessagePattern)
	githubRules.CommitAuthorEmailPattern = patternRuleToGitHub(rule.Rules.CommitAuthorEmailPattern)
	githubRules.CommitterEmailPattern = patternRuleToGitHub(rule.Rules.CommitterEmailPattern)
	githubRules.BranchNamePattern = patternRuleToGitHub(rule.Rules.BranchNamePattern)
	githubRules.TagNamePattern = patternRuleToGitHub(rule.Rules.TagNamePattern)
	if rule.Rules.CodeScanning != nil {
		tools := make([]*rulesets.CodeScanningTool, len(rule.Rules.CodeScanning.CodeScanningTools))
		for i, tool := range rule.Rules.CodeScanning.CodeScanningTools {
			tools[i] = &rulesets.CodeScanningTool{
				Tool:                    tool.Tool,
				AlertsThreshold:         tool.AlertsThreshold,
				SecurityAlertsThreshold: tool.SecurityAlertsThreshold,
			}
		}
		githubRules.CodeScanning = &rulesets.CodeScanningRuleParameters{CodeScanningTools: tools}
	}
	if rule.Rules.CodeQuality != nil {
		githubRules.CodeQuality = &rulesets.CodeQualityRuleParameters{Severity: rule.Rules.CodeQuality.Severity}
	}
	if cc := rule.Rules.CodeCoverage; cc != nil {
		minimum, err := decimalToGitHub(cc.MinimumCoverage)
		if err != nil {
			return nil, errors.Wrapf(err, "ruleset %s: minimumCoverage", rule.Name)
		}
		maxDrop, err := decimalToGitHub(cc.MaxCoverageDrop)
		if err != nil {
			return nil, errors.Wrapf(err, "ruleset %s: maxCoverageDrop", rule.Name)
		}
		githubRules.CodeCoverage = &rulesets.CodeCoverageRuleParameters{MinimumCoverage: minimum, MaxCoverageDrop: maxDrop}
	}
	if rule.Rules.CopilotCodeReview != nil {
		githubRules.CopilotCodeReview = &rulesets.CopilotCodeReviewRuleParameters{
			ReviewOnPush:            *rule.Rules.CopilotCodeReview.ReviewOnPush,
			ReviewDraftPullRequests: *rule.Rules.CopilotCodeReview.ReviewDraftPullRequests,
		}
	}
	if rule.Rules.FileExtensionRestriction != nil {
		githubRules.FileExtensionRestriction = &rulesets.FileExtensionRestrictionRuleParameters{
			RestrictedFileExtensions: rule.Rules.FileExtensionRestriction.RestrictedFileExtensions,
		}
	}
	if rule.Rules.FilePathRestriction != nil {
		githubRules.FilePathRestriction = &rulesets.FilePathRestrictionRuleParameters{
			RestrictedFilePaths: rule.Rules.FilePathRestriction.RestrictedFilePaths,
			IgnoredFilePaths:    rule.Rules.FilePathRestriction.IgnoredFilePaths,
		}
	}
	if rule.Rules.MaxFilePathLength != nil {
		githubRules.MaxFilePathLength = &rulesets.MaxFilePathLengthRuleParameters{MaxFilePathLength: rule.Rules.MaxFilePathLength.MaxFilePathLength}
	}
	if rule.Rules.MaxFileSize != nil {
		githubRules.MaxFileSize = &rulesets.MaxFileSizeRuleParameters{
			MaxFileSize:      rule.Rules.MaxFileSize.MaxFileSize,
			IgnoredFilePaths: rule.Rules.MaxFileSize.IgnoredFilePaths,
		}
	}
	rules, err := githubRules.Encode()
	if err != nil {
		return nil, err
	}
	githubRuleset.Rules = rules
	return githubRuleset, nil
}

// patternRuleToGitHub converts a CR pattern rule into the GitHub API form.
func patternRuleToGitHub(p *v1alpha1.RulesPattern) *rulesets.PatternRuleParameters {
	if p == nil {
		return nil
	}
	return &rulesets.PatternRuleParameters{
		Name:     p.Name,
		Negate:   p.Negate,
		Operator: p.Operator,
		Pattern:  p.Pattern,
	}
}

const (
	// rulesetSourceRepository is the source_type of a ruleset the repository holds itself.
	rulesetSourceRepository = "Repository"
	rulesetTargetPush       = "push"

	// refNameInclude and refNameExclude name the lists of a ruleset's ref conditions.
	refNameInclude = "include"
	refNameExclude = "exclude"

	// secretTypeProviderPatterns is the secret type GitHub applies when none is listed.
	secretTypeProviderPatterns v1alpha1.RulesSecretType = "provider_patterns"

	// The merge methods a pull request may be merged with, all allowed when none is listed.
	mergeMethodMerge  v1alpha1.RulesMergeMethod = "merge"
	mergeMethodSquash v1alpha1.RulesMergeMethod = "squash"
	mergeMethodRebase v1alpha1.RulesMergeMethod = "rebase"
)

// rulesetConditions returns a ruleset's conditions in the form that is compared and
// sent. A push ruleset gets nil, as GitHub stores it. Other rulesets get both ref
// lists, sorted.
func rulesetConditions(target string, c *v1alpha1.RulesetConditions) *v1alpha1.RulesetConditions {
	if target == rulesetTargetPush {
		return nil
	}
	refName := &v1alpha1.RulesetRefName{Include: []string{}, Exclude: []string{}}
	if c != nil && c.RefName != nil {
		refName.Include = util.SortAndReturn(slices.Clone(util.DefaultToStringSlice(c.RefName.Include)))
		refName.Exclude = util.SortAndReturn(slices.Clone(util.DefaultToStringSlice(c.RefName.Exclude)))
	}
	return &v1alpha1.RulesetConditions{RefName: refName}
}

// rulesetBypassActors returns bypass actors in the form that is compared and sent:
// sorted, the mode defaulting to "always", and nil for an empty list.
func rulesetBypassActors(actors []*v1alpha1.RulesetByPassActors) []*v1alpha1.RulesetByPassActors {
	if len(actors) == 0 {
		return nil
	}
	out := make([]*v1alpha1.RulesetByPassActors, len(actors))
	for i, actor := range actors {
		out[i] = actor.DeepCopy()
		out[i].BypassMode = util.StringDerefToPointer(actor.BypassMode, "always")
	}
	util.SortRulesBypassActors(out)
	return out
}

// duplicateBypassActor returns an error naming the first actor the ruleset lists twice.
// GitHub stores each actor once, so the CR must list each once too. Actors match on type,
// mode and ID; OrganizationAdmin and DeployKey match on type and mode, as GitHub stores
// their ID as null. The actors come from rulesetBypassActors, so each mode is set.
func duplicateBypassActor(ruleset string, actors []*v1alpha1.RulesetByPassActors) error {
	seen := map[string]bool{}
	for _, a := range actors {
		actor := pointer.Deref(a.ActorType, "")
		if !actorIDIgnored(a.ActorType) && a.ActorID != nil {
			actor += fmt.Sprintf(" %d", *a.ActorID)
		}
		key := actor + "/" + pointer.Deref(a.BypassMode, "")
		if seen[key] {
			return errors.Errorf("ruleset %s lists bypass actor %s twice with bypassMode %s", ruleset, actor, pointer.Deref(a.BypassMode, ""))
		}
		seen[key] = true
	}
	return nil
}

// duplicateRefPattern returns an error naming the first ref pattern the ruleset lists twice
// in include or exclude. GitHub stores each pattern once, so the CR must list each once too.
// The conditions come from rulesetConditions, so both lists are sorted.
func duplicateRefPattern(ruleset string, c *v1alpha1.RulesetConditions) error {
	if c == nil {
		return nil
	}
	for _, list := range []struct {
		name     string
		patterns []string
	}{{refNameInclude, c.RefName.Include}, {refNameExclude, c.RefName.Exclude}} {
		for i := 1; i < len(list.patterns); i++ {
			if list.patterns[i] == list.patterns[i-1] {
				return errors.Errorf("ruleset %s lists ref pattern %s twice in %s", ruleset, list.patterns[i], list.name)
			}
		}
	}
	return nil
}

// actorIDIgnored reports whether GitHub stores this actor type with a null actor_id.
func actorIDIgnored(actorType *string) bool {
	t := pointer.Deref(actorType, "")
	return t == "OrganizationAdmin" || t == "DeployKey"
}

// secretTypeSet returns secret types in the form that is compared and sent: sorted.
// An empty list becomes provider_patterns, GitHub's default.
func secretTypeSet(types []v1alpha1.RulesSecretType) []v1alpha1.RulesSecretType {
	if len(types) == 0 {
		return []v1alpha1.RulesSecretType{secretTypeProviderPatterns}
	}
	out := slices.Clone(types)
	slices.Sort(out)
	return out
}

// withoutUndeclared gives gh, GitHub's form of the ruleset, the CR's values for fields
// GitHub fills in by itself: OrganizationAdmin and DeployKey actor IDs and a missing
// update_allows_fetch_and_merge. It sorts gh's bypass actors.
func withoutUndeclared(gh *v1alpha1.RepositoryRuleset, cr v1alpha1.RepositoryRuleset) {
	for _, actor := range gh.BypassActors {
		if !actorIDIgnored(actor.ActorType) {
			continue
		}
		i := slices.IndexFunc(cr.BypassActors, func(c *v1alpha1.RulesetByPassActors) bool {
			return pointer.Deref(c.ActorType, "") == *actor.ActorType && pointer.Deref(c.BypassMode, "") == pointer.Deref(actor.BypassMode, "")
		})
		if i >= 0 {
			actor.ActorID = cr.BypassActors[i].ActorID
		}
	}
	util.SortRulesBypassActors(gh.BypassActors)

	if gh.Rules == nil || cr.Rules == nil {
		return
	}
	if gh.Rules.UpdateAllowsFetchAndMerge == nil && *gh.Rules.Update {
		gh.Rules.UpdateAllowsFetchAndMerge = cr.Rules.UpdateAllowsFetchAndMerge
	}
}

// pullRequestDefaults fills a pull_request rule's unset parameters with GitHub's defaults
// and sorts its merge methods, dismissal actors and required reviewers, which GitHub
// treats as sets. It is applied to the CR and to GitHub's form alike, so they compare equal.
func pullRequestDefaults(p *v1alpha1.RulesPullRequest) {
	p.DismissStaleReviewsOnPush = util.BoolDerefToPointer(p.DismissStaleReviewsOnPush, false)
	p.RequireCodeOwnerReview = util.BoolDerefToPointer(p.RequireCodeOwnerReview, false)
	p.RequireLastPushApproval = util.BoolDerefToPointer(p.RequireLastPushApproval, false)
	p.RequiredReviewThreadResolution = util.BoolDerefToPointer(p.RequiredReviewThreadResolution, false)
	p.RequiredApprovingReviewCount = util.IntDerefToPointer(p.RequiredApprovingReviewCount, 0)

	if len(p.AllowedMergeMethods) == 0 {
		p.AllowedMergeMethods = []v1alpha1.RulesMergeMethod{mergeMethodMerge, mergeMethodSquash, mergeMethodRebase}
	}
	p.AllowedMergeMethods = slices.Clone(p.AllowedMergeMethods)
	slices.Sort(p.AllowedMergeMethods)

	if p.DismissalRestriction == nil {
		p.DismissalRestriction = &v1alpha1.RulesDismissalRestriction{}
	}
	if p.DismissalRestriction.AllowedActors == nil {
		p.DismissalRestriction.AllowedActors = []*v1alpha1.RulesDismissalActor{}
	}
	slices.SortFunc(p.DismissalRestriction.AllowedActors, func(a, b *v1alpha1.RulesDismissalActor) int {
		return stdcmp.Or(strings.Compare(a.Type, b.Type), stdcmp.Compare(a.ID, b.ID))
	})

	if p.RequiredReviewers == nil {
		p.RequiredReviewers = []*v1alpha1.RulesRequiredReviewer{}
	}
	for _, r := range p.RequiredReviewers {
		r.FilePatterns = util.DefaultToStringSlice(r.FilePatterns)
	}
	slices.SortFunc(p.RequiredReviewers, func(a, b *v1alpha1.RulesRequiredReviewer) int {
		return stdcmp.Or(
			strings.Compare(a.Reviewer.Type, b.Reviewer.Type),
			stdcmp.Compare(a.Reviewer.ID, b.Reviewer.ID),
			stdcmp.Compare(a.MinimumApprovals, b.MinimumApprovals),
			slices.Compare(a.FilePatterns, b.FilePatterns),
		)
	})
}

// pullRequestFromGitHub converts GitHub's pull_request parameters into their CR form,
// with defaults filled in. An actor id that is missing or not an integer is an error.
func pullRequestFromGitHub(params *rulesets.PullRequestRuleParameters) (*v1alpha1.RulesPullRequest, error) {
	p := &v1alpha1.RulesPullRequest{
		RequireCodeOwnerReview:                     pointer.To(params.RequireCodeOwnerReview),
		RequireLastPushApproval:                    pointer.To(params.RequireLastPushApproval),
		RequiredReviewThreadResolution:             pointer.To(params.RequiredReviewThreadResolution),
		RequiredApprovingReviewCount:               pointer.To(params.RequiredApprovingReviewCount),
		DismissStaleReviewsOnPush:                  pointer.To(params.DismissStaleReviewsOnPush),
		RequireExtraApprovalForUnattributedChanges: params.RequireExtraApprovalForUnattributedChanges,
	}
	for _, m := range params.AllowedMergeMethods {
		p.AllowedMergeMethods = append(p.AllowedMergeMethods, v1alpha1.RulesMergeMethod(m))
	}
	if dr := params.DismissalRestriction; dr != nil {
		p.DismissalRestriction = &v1alpha1.RulesDismissalRestriction{Enabled: dr.Enabled}
		for _, a := range dr.AllowedActors {
			id, err := a.ID.Int64()
			if err != nil {
				return nil, errors.Wrap(err, "dismissal actor id")
			}
			p.DismissalRestriction.AllowedActors = append(p.DismissalRestriction.AllowedActors, &v1alpha1.RulesDismissalActor{ID: id, Type: a.Type})
		}
	}
	for _, r := range params.RequiredReviewers {
		id, err := r.Reviewer.ID.Int64()
		if err != nil {
			return nil, errors.Wrap(err, "required reviewer id")
		}
		p.RequiredReviewers = append(p.RequiredReviewers, &v1alpha1.RulesRequiredReviewer{
			FilePatterns:     slices.Clone(r.FilePatterns),
			MinimumApprovals: r.MinimumApprovals,
			Reviewer:         v1alpha1.RulesReviewer{ID: id, Type: r.Reviewer.Type},
		})
	}
	pullRequestDefaults(p)
	return p, nil
}

// pullRequestToGitHub converts a defaulted pull_request rule into GitHub's form. Every
// modelled parameter is sent, defaults included.
func pullRequestToGitHub(p *v1alpha1.RulesPullRequest) *rulesets.PullRequestRuleParameters {
	params := &rulesets.PullRequestRuleParameters{
		AllowedMergeMethods:            make([]string, len(p.AllowedMergeMethods)),
		DismissStaleReviewsOnPush:      *p.DismissStaleReviewsOnPush,
		DismissalRestriction:           &rulesets.DismissalRestriction{Enabled: p.DismissalRestriction.Enabled, AllowedActors: make([]*rulesets.Actor, len(p.DismissalRestriction.AllowedActors))},
		RequireCodeOwnerReview:         *p.RequireCodeOwnerReview,
		RequireLastPushApproval:        *p.RequireLastPushApproval,
		RequiredApprovingReviewCount:   *p.RequiredApprovingReviewCount,
		RequiredReviewThreadResolution: *p.RequiredReviewThreadResolution,
		RequiredReviewers:              make([]*rulesets.RequiredReviewer, len(p.RequiredReviewers)),

		RequireExtraApprovalForUnattributedChanges: p.RequireExtraApprovalForUnattributedChanges,
	}
	for i, m := range p.AllowedMergeMethods {
		params.AllowedMergeMethods[i] = string(m)
	}
	for i, a := range p.DismissalRestriction.AllowedActors {
		params.DismissalRestriction.AllowedActors[i] = &rulesets.Actor{ID: json.Number(strconv.FormatInt(a.ID, 10)), Type: a.Type}
	}
	for i, r := range p.RequiredReviewers {
		params.RequiredReviewers[i] = &rulesets.RequiredReviewer{
			FilePatterns:     r.FilePatterns,
			MinimumApprovals: r.MinimumApprovals,
			Reviewer:         rulesets.Actor{ID: json.Number(strconv.FormatInt(r.Reviewer.ID, 10)), Type: r.Reviewer.Type},
		}
	}
	return params
}

// canonicalDecimal returns the decimal number s in its shortest form, "80.0" as "80",
// so equal numbers compare equal. A non-number is returned as is.
func canonicalDecimal(s *string) *string {
	if s == nil {
		return nil
	}
	f, err := strconv.ParseFloat(*s, 64)
	if err != nil {
		return s
	}
	return pointer.To(strconv.FormatFloat(f, 'f', -1, 64))
}

// decimalFromGitHub converts a number GitHub returns into its CR form, a decimal string.
func decimalFromGitHub(f *float64) *string {
	if f == nil {
		return nil
	}
	return pointer.To(strconv.FormatFloat(*f, 'f', -1, 64))
}

// decimalToGitHub converts a CR decimal string into the number GitHub takes.
func decimalToGitHub(s *string) (*float64, error) {
	if s == nil {
		return nil, nil
	}
	f, err := strconv.ParseFloat(*s, 64)
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// maxRulesetsPerRepository is GitHub's per-repository limit on rulesets.
const maxRulesetsPerRepository = 75

// updateRepositoryRules synchronizes the repository rules of a GitHub repository
// to match with those detailed in the repository resource object.
// It performs necessary additions, updates, or deletions based on the difference between
// the actual state on GitHub and the desired state in the resource object.
func updateRepositoryRules(ctx context.Context, cr *v1alpha1.Repository, gh *ghclient.Client, repoName string) error {
	// Generate a map of the repository rules from the Crossplane resource
	crRToConfig, err := getRepositoryRulesMapFromCr(*cr.Spec.ForProvider.RepositoryRules)
	if err != nil {
		// The spec is invalid; Observe reports it through RulesetsPartial.
		return nil
	}
	// Fetch the current repository rules from GitHub
	ghRepoRules, err := getRepositoryRules(ctx, gh, cr.Spec.ForProvider.Org, repoName)
	if ghclient.Is403(err) {
		// GitHub refuses rulesets on this repository; Observe reports it through RulesetsPartial.
		return nil
	}
	if err != nil {
		return err
	}
	// Generate a map of the repository rules from GitHub. Rulesets holding rule types the
	// provider does not model are left out of it and named in report.skipped.
	ghRToConfig, report, err := getRepositoryRulesWithConfig(ctx, gh, cr.Spec.ForProvider.Org, repoName, ghRepoRules, crRToConfig)
	if err != nil {
		return err
	}
	applyRulesetReport(crRToConfig, report)
	// Determine which rules need to be deleted, added, or updated
	toDelete, toAdd, toUpdate := util.DiffRepositoryRulesets(ghRToConfig, crRToConfig)

	// Delete the rules that are no longer needed
	deleteRules := func() error {
		for name := range toDelete {
			rulesetID, _ := findRulesetIDByName(ghRepoRules, name)
			_, err = gh.Rulesets.DeleteRuleset(ctx, cr.Spec.ForProvider.Org, repoName, rulesetID)
			if err != nil {
				return err
			}
		}
		return nil
	}
	// Create and update before deleting, so a failed create leaves the old ruleset in place.
	// Over GitHub's cap the surplus rulesets go first, so the creates fit.
	deleteFirst := len(ghRepoRules)+len(toAdd) > maxRulesetsPerRepository
	if deleteFirst {
		if err := deleteRules(); err != nil {
			return err
		}
	}
	// Add the new rules
	for _, rule := range toAdd {
		ruleset, err := crRepoRulesToRulesConfig(rule)
		if err != nil {
			return err
		}
		_, _, err = gh.Rulesets.CreateRuleset(ctx, cr.Spec.ForProvider.Org, repoName, *ruleset)
		if err != nil {
			return err
		}
	}
	// Update the existing rules
	for name, rule := range toUpdate {
		rulesetID, _ := findRulesetIDByName(ghRepoRules, name)
		ruleset, err := crRepoRulesToRulesConfig(rule)
		if err != nil {
			return err
		}
		_, _, err = gh.Rulesets.UpdateRuleset(ctx, cr.Spec.ForProvider.Org, repoName, rulesetID, *ruleset)
		if err != nil {
			return err
		}
	}
	if !deleteFirst {
		return deleteRules()
	}
	return nil
}

// findRulesetIDByName iterates over a slice of GitHub Ruleset pointers and returns the ID of the ruleset
// that matches the provided name. If no match is found, it returns an error.
func findRulesetIDByName(ghRulesets []*rulesets.Ruleset, name string) (int64, error) {
	for _, ruleset := range ghRulesets {
		if ruleset.Name == name {
			return *ruleset.ID, nil
		}
	}
	return 0, errors.Errorf("ruleset with name %s not found", name)
}

//nolint:gocyclo
func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	cr, ok := mg.(*v1alpha1.Repository)
	if !ok {
		return managed.ExternalUpdate{}, errors.New(errNotRepository)
	}

	name := meta.GetExternalName(cr)

	archivedCr := pointer.Deref(cr.Spec.ForProvider.Archived, false)

	repo, _, err := c.github.Repositories.Get(ctx, cr.Spec.ForProvider.Org, name)
	if err != nil {
		return managed.ExternalUpdate{}, err
	}

	// Archived repos freeze settings, branch protection, rulesets and webhooks (and
	// collaborator additions). Reconcile only what GitHub still permits while archived:
	// team access, topics and collaborator removals.
	archivedGh := pointer.Deref(repo.Archived, false)
	if archivedCr {
		if !archivedGh {
			if _, _, err = c.github.Repositories.Edit(ctx, cr.Spec.ForProvider.Org, name, &github.Repository{Archived: pointer.To(true)}); err != nil {
				return managed.ExternalUpdate{}, err
			}
		}
		if err = updateRepoTeams(ctx, cr, c.github, name); err != nil {
			return managed.ExternalUpdate{}, err
		}
		if err = removeArchivedRepoUsers(ctx, cr, c.github, name); err != nil {
			return managed.ExternalUpdate{}, err
		}
		if cr.Spec.ForProvider.Topics != nil {
			if _, _, err = c.github.Repositories.ReplaceAllTopics(ctx, cr.Spec.ForProvider.Org, name, cr.Spec.ForProvider.Topics); err != nil {
				return managed.ExternalUpdate{}, err
			}
		}
		return managed.ExternalUpdate{}, nil
	}
	if archivedGh {
		// Unarchive first so the setting writes below are accepted.
		if _, _, err = c.github.Repositories.Edit(ctx, cr.Spec.ForProvider.Org, name, &github.Repository{Archived: pointer.To(false)}); err != nil {
			return managed.ExternalUpdate{}, err
		}
	}

	editReq := editRequest(cr, repo, name)
	echoed, _, err := c.github.Repositories.Edit(ctx, cr.Spec.ForProvider.Org, name, editReq)
	if err != nil {
		return managed.ExternalUpdate{}, err
	}
	cr.Status.AtProvider.UnappliedSettings = unappliedSettings(editReq, echoed)

	err = updateRepoUsers(ctx, cr, c.github, name)
	if err != nil {
		return managed.ExternalUpdate{}, err
	}

	err = updateRepoTeams(ctx, cr, c.github, name)
	if err != nil {
		return managed.ExternalUpdate{}, err
	}

	if cr.Spec.ForProvider.Webhooks != nil {
		err = updateRepoWebhooks(c, ctx, cr, c.github, name)
		if err != nil {
			return managed.ExternalUpdate{}, err
		}
	}

	if cr.Spec.ForProvider.BranchProtectionRules != nil {
		err = updateProtectedBranches(ctx, cr, c.github, name)
		if err != nil {
			return managed.ExternalUpdate{}, err
		}
	}
	if cr.Spec.ForProvider.RepositoryRules != nil {
		err = updateRepositoryRules(ctx, cr, c.github, name)
		if err != nil {
			return managed.ExternalUpdate{}, err
		}

	}

	// Update topics if specified
	if cr.Spec.ForProvider.Topics != nil {
		_, _, err = c.github.Repositories.ReplaceAllTopics(ctx, cr.Spec.ForProvider.Org, name, cr.Spec.ForProvider.Topics)
		if err != nil {
			return managed.ExternalUpdate{}, err
		}
	}

	return managed.ExternalUpdate{}, nil
}

func (c *external) Delete(ctx context.Context, mg resource.Managed) (managed.ExternalDelete, error) {
	cr, ok := mg.(*v1alpha1.Repository)
	if !ok {
		return managed.ExternalDelete{}, errors.New(errNotRepository)
	}

	name := meta.GetExternalName(cr)

	forceDelete := pointer.Deref(cr.Spec.ForProvider.ForceDelete, false)
	if !forceDelete {
		return managed.ExternalDelete{}, errors.New("You can only delete repositories by setting `forceDelete: true`")
	}

	_, err := c.github.Repositories.Delete(ctx, cr.Spec.ForProvider.Org, name)
	if err != nil {
		return managed.ExternalDelete{}, err
	}

	if c.metrics != nil {
		c.metrics.ForgetRepository(cr.Spec.ForProvider.Org, name)
	}

	return managed.ExternalDelete{}, nil
}

// Disconnect is a no-op: the GitHub client is cached and shared across
// reconciles, so there is nothing to release per connection.
func (c *external) Disconnect(_ context.Context) error {
	return nil
}
