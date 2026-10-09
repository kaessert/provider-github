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
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-github/v90/github"

	"github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	ghclient "github.com/crossplane/provider-github/internal/clients"
	"github.com/crossplane/provider-github/internal/clients/fake"

	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	"github.com/crossplane/crossplane-runtime/v2/pkg/test"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
)

// Unlike many Kubernetes projects Crossplane does not use third party testing
// libraries, per the common Go test review comments. Crossplane encourages the
// use of table driven unit tests. The tests of the crossplane-runtime project
// are representative of the testing style Crossplane encourages.
//
// https://github.com/golang/go/wiki/TestComments
// https://github.com/crossplane/crossplane/blob/master/CONTRIBUTING.md#contributing-code

var (
	teamPrivacy     = "secret"
	teamDescription = "description"

	member1     = "test-user-1"
	member1Role = "maintainer"
	member2     = "test-user-2"
	member2Role = "member"
	member3     = "test-user-3"
	member3Role = "member"
)

type teamModifier func(*v1alpha1.Team)

func withExtraMember(user, role string) teamModifier {
	return func(cr *v1alpha1.Team) {
		cr.Spec.ForProvider.Members = append(cr.Spec.ForProvider.Members, v1alpha1.TeamMemberUser{
			User: user,
			Role: role,
		})
	}
}

func withSoleMember(user, role string) teamModifier {
	return func(cr *v1alpha1.Team) {
		cr.Spec.ForProvider.Members = []v1alpha1.TeamMemberUser{{User: user, Role: role}}
	}
}

// func withProperty() teamModifier {
// 	return func(r *v1alpha1.Team) {
// 		r.Spec.ForProvider.ConfigurableField = "value"
// 	}
// }

func team(m ...teamModifier) *v1alpha1.Team {
	cr := &v1alpha1.Team{}

	cr.Spec.ForProvider.Description = teamDescription
	cr.Spec.ForProvider.Members = []v1alpha1.TeamMemberUser{
		{
			User: member1,
			Role: member1Role,
		},
		{
			User: member2,
			Role: member2Role,
		},
	}

	meta.SetExternalName(cr, "")

	for _, f := range m {
		f(cr)
	}
	return cr
}

func githubTeam(role string) []*github.User {
	if role == "maintainer" {
		return []*github.User{
			{
				Login: &member1,
			},
		}
	}

	// Role is member here because there are only
	// member and maintainer
	return []*github.User{
		{
			Login: &member2,
		},
	}
}

func TestObserve(t *testing.T) {
	type fields struct {
		github *ghclient.Client
	}

	type args struct {
		ctx context.Context
		mg  resource.Managed
	}

	type want struct {
		o   managed.ExternalObservation
		err error
		// partialReason: if non-empty, asserts TeamMembershipPartial reason after Observe.
		partialReason xpv2.ConditionReason
	}

	cases := map[string]struct {
		reason string
		fields fields
		args   args
		want   want
	}{
		"UpToDate": {
			fields: fields{
				github: &ghclient.Client{
					Services: &ghclient.Services{
						Teams: &fake.MockTeamsClient{
							MockGetTeamBySlug: func(ctx context.Context, org, slug string) (*github.Team, *github.Response, error) {
								return &github.Team{
									Privacy:     &teamPrivacy,
									Description: &teamDescription,
								}, nil, nil
							},
							MockListTeamMembersBySlug: func(ctx context.Context, org, slug string, opts *github.TeamListTeamMembersOptions) ([]*github.User, *github.Response, error) {
								return githubTeam(opts.Role), fake.GenerateEmptyResponse(), nil
							},
							MockListPendingTeamInvitationsBySlug: func(ctx context.Context, org, slug string, opts *github.ListOptions) ([]*github.Invitation, *github.Response, error) {
								return nil, fake.GenerateEmptyResponse(), nil
							},
							MockListChildTeamsByParentSlug: func(ctx context.Context, org, slug string, opts *github.ListOptions) ([]*github.Team, *github.Response, error) {
								return nil, fake.GenerateEmptyResponse(), nil
							},
						},
					},
				},
			},
			args: args{
				mg: team(),
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: true,
				},
				err: nil,
			},
		},
		// A pending team invitation is in-flight work, not drift.
		"UpToDate_PendingInviteFulfillsDeclaredMember": {
			fields: fields{
				github: &ghclient.Client{
					Services: &ghclient.Services{
						Teams: &fake.MockTeamsClient{
							MockGetTeamBySlug: func(ctx context.Context, org, slug string) (*github.Team, *github.Response, error) {
								return &github.Team{
									Privacy:     &teamPrivacy,
									Description: &teamDescription,
								}, nil, nil
							},
							MockListTeamMembersBySlug: func(ctx context.Context, org, slug string, opts *github.TeamListTeamMembersOptions) ([]*github.User, *github.Response, error) {
								return githubTeam(opts.Role), fake.GenerateEmptyResponse(), nil
							},
							MockListPendingTeamInvitationsBySlug: func(ctx context.Context, org, slug string, opts *github.ListOptions) ([]*github.Invitation, *github.Response, error) {
								return []*github.Invitation{
									{Login: &member3},
								}, fake.GenerateEmptyResponse(), nil
							},
							MockListChildTeamsByParentSlug: func(ctx context.Context, org, slug string, opts *github.ListOptions) ([]*github.Team, *github.Response, error) {
								return nil, fake.GenerateEmptyResponse(), nil
							},
						},
					},
				},
			},
			args: args{
				mg: team(withExtraMember(member3, member3Role)),
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: true,
				},
				err:           nil,
				partialReason: reasonPendingTeamInvitation,
			},
		},
		// Active org member declared on team but absent must drift to inviteable.
		"Drift_DeclaredMemberIsActiveOrgMember": {
			fields: fields{
				github: &ghclient.Client{
					Services: &ghclient.Services{
						Teams: &fake.MockTeamsClient{
							MockGetTeamBySlug: func(ctx context.Context, org, slug string) (*github.Team, *github.Response, error) {
								return &github.Team{
									Privacy:     &teamPrivacy,
									Description: &teamDescription,
								}, nil, nil
							},
							MockListTeamMembersBySlug: func(ctx context.Context, org, slug string, opts *github.TeamListTeamMembersOptions) ([]*github.User, *github.Response, error) {
								return githubTeam(opts.Role), fake.GenerateEmptyResponse(), nil
							},
							MockListPendingTeamInvitationsBySlug: func(ctx context.Context, org, slug string, opts *github.ListOptions) ([]*github.Invitation, *github.Response, error) {
								return nil, fake.GenerateEmptyResponse(), nil
							},
							MockListChildTeamsByParentSlug: func(ctx context.Context, org, slug string, opts *github.ListOptions) ([]*github.Team, *github.Response, error) {
								return nil, fake.GenerateEmptyResponse(), nil
							},
						},
						Organizations: &fake.MockOrganizationsClient{
							MockGetOrgMembership: func(ctx context.Context, user, org string) (*github.Membership, *github.Response, error) {
								active := "active"
								return &github.Membership{State: &active}, fake.GenerateEmptyResponse(), nil
							},
						},
					},
				},
			},
			args: args{
				mg: team(withExtraMember(member3, member3Role)),
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: false,
				},
				err: nil,
			},
		},
		// A non-admin's direct maintainer grant on the parent, layered on their
		// inherited child membership, is a rogue override: the DELETE strips the
		// direct grant and demotes them to inherited member, so toRemove must fire.
		// (Contrast UpToDate_InheritedOrgAdminMaintainerSkipped, where the maintainer
		// status is org-admin-derived and unremovable.)
		"Drift_DirectOverrideOnChildInheritedUser": {
			fields: fields{
				github: &ghclient.Client{
					Services: &ghclient.Services{
						Teams: &fake.MockTeamsClient{
							MockGetTeamBySlug: func(ctx context.Context, org, slug string) (*github.Team, *github.Response, error) {
								return &github.Team{
									Privacy:     &teamPrivacy,
									Description: &teamDescription,
								}, nil, nil
							},
							MockListTeamMembersBySlug: func(ctx context.Context, org, slug string, opts *github.TeamListTeamMembersOptions) ([]*github.User, *github.Response, error) {
								// Parent (slug==""): member3 appears as MAINTAINER (direct
								// override), not as member (which is the child's role).
								if slug == "" {
									if opts.Role == "maintainer" {
										return []*github.User{{Login: &member1}, {Login: &member3}}, fake.GenerateEmptyResponse(), nil
									}
									return []*github.User{{Login: &member2}}, fake.GenerateEmptyResponse(), nil
								}
								// Child team: member3 as member only.
								if opts.Role == "member" {
									return []*github.User{{Login: &member3}}, fake.GenerateEmptyResponse(), nil
								}
								return nil, fake.GenerateEmptyResponse(), nil
							},
							MockListPendingTeamInvitationsBySlug: func(ctx context.Context, org, slug string, opts *github.ListOptions) ([]*github.Invitation, *github.Response, error) {
								return nil, fake.GenerateEmptyResponse(), nil
							},
							MockListChildTeamsByParentSlug: func(ctx context.Context, org, slug string, opts *github.ListOptions) ([]*github.Team, *github.Response, error) {
								if slug == "" {
									childSlug := "child-team"
									return []*github.Team{{Slug: &childSlug}}, fake.GenerateEmptyResponse(), nil
								}
								return nil, fake.GenerateEmptyResponse(), nil
							},
						},
						Organizations: &fake.MockOrganizationsClient{
							MockGetOrgMembership: func(ctx context.Context, user, org string) (*github.Membership, *github.Response, error) {
								role := "member"
								return &github.Membership{Role: &role}, fake.GenerateEmptyResponse(), nil
							},
						},
					},
				},
			},
			args: args{
				mg: team(),
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: false,
				},
				err: nil,
			},
		},
		// An org admin appears in the parent rollup as maintainer (org admins are
		// maintainers of every team they belong to) while being inherited from a
		// child and absent from the CR. A parent-level DELETE cannot strip org-admin
		// maintainer status, so flagging toRemove no-ops and reconcile-loops forever.
		"UpToDate_InheritedOrgAdminMaintainerSkipped": {
			fields: fields{
				github: &ghclient.Client{
					Services: &ghclient.Services{
						Teams: &fake.MockTeamsClient{
							MockGetTeamBySlug: func(ctx context.Context, org, slug string) (*github.Team, *github.Response, error) {
								return &github.Team{
									Privacy:     &teamPrivacy,
									Description: &teamDescription,
								}, nil, nil
							},
							MockListTeamMembersBySlug: func(ctx context.Context, org, slug string, opts *github.TeamListTeamMembersOptions) ([]*github.User, *github.Response, error) {
								// Parent (slug==""): member3 appears as maintainer (org-admin
								// derived), not in the CR; member1/member2 match the CR.
								if slug == "" {
									if opts.Role == "maintainer" {
										return []*github.User{{Login: &member1}, {Login: &member3}}, fake.GenerateEmptyResponse(), nil
									}
									return []*github.User{{Login: &member2}}, fake.GenerateEmptyResponse(), nil
								}
								// Child team: member3 is a member (the inheritance source).
								if opts.Role == "member" {
									return []*github.User{{Login: &member3}}, fake.GenerateEmptyResponse(), nil
								}
								return nil, fake.GenerateEmptyResponse(), nil
							},
							MockListPendingTeamInvitationsBySlug: func(ctx context.Context, org, slug string, opts *github.ListOptions) ([]*github.Invitation, *github.Response, error) {
								return nil, fake.GenerateEmptyResponse(), nil
							},
							MockListChildTeamsByParentSlug: func(ctx context.Context, org, slug string, opts *github.ListOptions) ([]*github.Team, *github.Response, error) {
								if slug == "" {
									childSlug := "child-team"
									return []*github.Team{{Slug: &childSlug}}, fake.GenerateEmptyResponse(), nil
								}
								return nil, fake.GenerateEmptyResponse(), nil
							},
						},
						Organizations: &fake.MockOrganizationsClient{
							MockGetOrgMembership: func(ctx context.Context, user, org string) (*github.Membership, *github.Response, error) {
								role := "admin"
								state := "active"
								return &github.Membership{Role: &role, State: &state}, fake.GenerateEmptyResponse(), nil
							},
						},
					},
				},
			},
			args: args{
				mg: team(),
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: true,
				},
				err: nil,
			},
		},
		// Inherited-only user at role=member must not be flagged toRemove —
		// DELETE on a purely-inherited membership silently no-ops and would loop.
		"UpToDate_InheritedOnlyMemberSkipsToRemove": {
			fields: fields{
				github: &ghclient.Client{
					Services: &ghclient.Services{
						Teams: &fake.MockTeamsClient{
							MockGetTeamBySlug: func(ctx context.Context, org, slug string) (*github.Team, *github.Response, error) {
								return &github.Team{
									Privacy:     &teamPrivacy,
									Description: &teamDescription,
								}, nil, nil
							},
							MockListTeamMembersBySlug: func(ctx context.Context, org, slug string, opts *github.TeamListTeamMembersOptions) ([]*github.User, *github.Response, error) {
								// Parent (slug==""): member1 direct maintainer, member3 as member (inherited only).
								if slug == "" {
									if opts.Role == "maintainer" {
										return []*github.User{{Login: &member1}}, fake.GenerateEmptyResponse(), nil
									}
									return []*github.User{{Login: &member3}}, fake.GenerateEmptyResponse(), nil
								}
								// Child: member3 as maintainer (mismatched role with parent's inheritance default).
								if opts.Role == "maintainer" {
									return []*github.User{{Login: &member3}}, fake.GenerateEmptyResponse(), nil
								}
								return nil, fake.GenerateEmptyResponse(), nil
							},
							MockListPendingTeamInvitationsBySlug: func(ctx context.Context, org, slug string, opts *github.ListOptions) ([]*github.Invitation, *github.Response, error) {
								return nil, fake.GenerateEmptyResponse(), nil
							},
							MockListChildTeamsByParentSlug: func(ctx context.Context, org, slug string, opts *github.ListOptions) ([]*github.Team, *github.Response, error) {
								if slug == "" {
									childSlug := "child-team"
									return []*github.Team{{Slug: &childSlug}}, fake.GenerateEmptyResponse(), nil
								}
								return nil, fake.GenerateEmptyResponse(), nil
							},
						},
					},
				},
			},
			args: args{
				mg: team(withSoleMember(member1, member1Role)),
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: true,
				},
				err: nil,
			},
		},
		// CR-declared user inherited at matching role must not loop on AddTeamMembership —
		// the parent rollup already contains them, no inviteable PUT needed.
		"UpToDate_CRDeclaredAlsoInheritedAtMatchingRole": {
			fields: fields{
				github: &ghclient.Client{
					Services: &ghclient.Services{
						Teams: &fake.MockTeamsClient{
							MockGetTeamBySlug: func(ctx context.Context, org, slug string) (*github.Team, *github.Response, error) {
								return &github.Team{
									Privacy:     &teamPrivacy,
									Description: &teamDescription,
								}, nil, nil
							},
							MockListTeamMembersBySlug: func(ctx context.Context, org, slug string, opts *github.TeamListTeamMembersOptions) ([]*github.User, *github.Response, error) {
								// Parent and child both report member3 as member.
								if opts.Role == "member" {
									return []*github.User{{Login: &member3}}, fake.GenerateEmptyResponse(), nil
								}
								return nil, fake.GenerateEmptyResponse(), nil
							},
							MockListPendingTeamInvitationsBySlug: func(ctx context.Context, org, slug string, opts *github.ListOptions) ([]*github.Invitation, *github.Response, error) {
								return nil, fake.GenerateEmptyResponse(), nil
							},
							MockListChildTeamsByParentSlug: func(ctx context.Context, org, slug string, opts *github.ListOptions) ([]*github.Team, *github.Response, error) {
								if slug == "" {
									childSlug := "child-team"
									return []*github.Team{{Slug: &childSlug}}, fake.GenerateEmptyResponse(), nil
								}
								return nil, fake.GenerateEmptyResponse(), nil
							},
						},
					},
				},
			},
			args: args{
				mg: team(withSoleMember(member3, member3Role)),
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: true,
				},
				err: nil,
			},
		},
		// Inherited child-team members must subtract out of the parent's rollup, else parent reconcile-fights forever.
		"UpToDate_ChildTeamRollupSubtracted": {
			fields: fields{
				github: &ghclient.Client{
					Services: &ghclient.Services{
						Teams: &fake.MockTeamsClient{
							MockGetTeamBySlug: func(ctx context.Context, org, slug string) (*github.Team, *github.Response, error) {
								return &github.Team{
									Privacy:     &teamPrivacy,
									Description: &teamDescription,
								}, nil, nil
							},
							MockListTeamMembersBySlug: func(ctx context.Context, org, slug string, opts *github.TeamListTeamMembersOptions) ([]*github.User, *github.Response, error) {
								// Parent (slug=="") rollup includes the CR-declared members
								// plus member3 inherited from the child team.
								if slug == "" {
									if opts.Role == "maintainer" {
										return []*github.User{{Login: &member1}}, fake.GenerateEmptyResponse(), nil
									}
									return []*github.User{{Login: &member2}, {Login: &member3}}, fake.GenerateEmptyResponse(), nil
								}
								// Child team contributes only member3 (the inherited one).
								if opts.Role == "member" {
									return []*github.User{{Login: &member3}}, fake.GenerateEmptyResponse(), nil
								}
								return nil, fake.GenerateEmptyResponse(), nil
							},
							MockListPendingTeamInvitationsBySlug: func(ctx context.Context, org, slug string, opts *github.ListOptions) ([]*github.Invitation, *github.Response, error) {
								return nil, fake.GenerateEmptyResponse(), nil
							},
							MockListChildTeamsByParentSlug: func(ctx context.Context, org, slug string, opts *github.ListOptions) ([]*github.Team, *github.Response, error) {
								childSlug := "child-team"
								return []*github.Team{{Slug: &childSlug}}, fake.GenerateEmptyResponse(), nil
							},
						},
					},
				},
			},
			args: args{
				mg: team(),
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: true,
				},
				err: nil,
			},
		},
		// Declared non-org-member must NOT PUT (org-invite side effect); surface via partial condition.
		"UpToDate_DeclaredButPendingOrgMembership": {
			fields: fields{
				github: &ghclient.Client{
					Services: &ghclient.Services{
						Teams: &fake.MockTeamsClient{
							MockGetTeamBySlug: func(ctx context.Context, org, slug string) (*github.Team, *github.Response, error) {
								return &github.Team{
									Privacy:     &teamPrivacy,
									Description: &teamDescription,
								}, nil, nil
							},
							MockListTeamMembersBySlug: func(ctx context.Context, org, slug string, opts *github.TeamListTeamMembersOptions) ([]*github.User, *github.Response, error) {
								return githubTeam(opts.Role), fake.GenerateEmptyResponse(), nil
							},
							MockListPendingTeamInvitationsBySlug: func(ctx context.Context, org, slug string, opts *github.ListOptions) ([]*github.Invitation, *github.Response, error) {
								return nil, fake.GenerateEmptyResponse(), nil
							},
							MockListChildTeamsByParentSlug: func(ctx context.Context, org, slug string, opts *github.ListOptions) ([]*github.Team, *github.Response, error) {
								return nil, fake.GenerateEmptyResponse(), nil
							},
						},
						Organizations: &fake.MockOrganizationsClient{
							MockGetOrgMembership: func(ctx context.Context, user, org string) (*github.Membership, *github.Response, error) {
								return nil, nil, fake.Generate404Response()
							},
						},
					},
				},
			},
			args: args{
				mg: team(withExtraMember(member3, member3Role)),
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: true,
				},
				err:           nil,
				partialReason: reasonPendingOrgMembership,
			},
		},
		// (GH=member, CR=maintainer) is a legitimate operator promotion — GitHub
		// will accept the PUT. The isOrgAdmin probe must not fire here; otherwise
		// every promotion costs an extra Organizations.GetOrgMembership call.
		"Drift_PromotionToMaintainerSkipsOrgAdminProbe": {
			fields: fields{
				github: &ghclient.Client{
					Services: &ghclient.Services{
						Teams: &fake.MockTeamsClient{
							MockGetTeamBySlug: func(ctx context.Context, org, slug string) (*github.Team, *github.Response, error) {
								return &github.Team{
									Privacy:     &teamPrivacy,
									Description: &teamDescription,
								}, nil, nil
							},
							MockListTeamMembersBySlug: func(ctx context.Context, org, slug string, opts *github.TeamListTeamMembersOptions) ([]*github.User, *github.Response, error) {
								if opts.Role == "member" {
									return []*github.User{{Login: &member1}}, fake.GenerateEmptyResponse(), nil
								}
								return nil, fake.GenerateEmptyResponse(), nil
							},
							MockListPendingTeamInvitationsBySlug: func(ctx context.Context, org, slug string, opts *github.ListOptions) ([]*github.Invitation, *github.Response, error) {
								return nil, fake.GenerateEmptyResponse(), nil
							},
							MockListChildTeamsByParentSlug: func(ctx context.Context, org, slug string, opts *github.ListOptions) ([]*github.Team, *github.Response, error) {
								return nil, fake.GenerateEmptyResponse(), nil
							},
						},
						Organizations: &fake.MockOrganizationsClient{
							MockGetOrgMembership: func(ctx context.Context, user, org string) (*github.Membership, *github.Response, error) {
								panic("GetOrgMembership must not be called for (ghRole=member, crRole=maintainer)")
							},
						},
					},
				},
			},
			args: args{
				mg: team(withSoleMember(member1, "maintainer")),
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: false,
				},
				err: nil,
			},
		},
		// Org admin with CR role != GH-enforced role must route to roleEnforced,
		// else Observe loops on no-op PUTs forever.
		"UpToDate_OrgAdminRoleEnforced": {
			fields: fields{
				github: &ghclient.Client{
					Services: &ghclient.Services{
						Teams: &fake.MockTeamsClient{
							MockGetTeamBySlug: func(ctx context.Context, org, slug string) (*github.Team, *github.Response, error) {
								return &github.Team{
									Privacy:     &teamPrivacy,
									Description: &teamDescription,
								}, nil, nil
							},
							MockListTeamMembersBySlug: func(ctx context.Context, org, slug string, opts *github.TeamListTeamMembersOptions) ([]*github.User, *github.Response, error) {
								if opts.Role == "maintainer" {
									return []*github.User{{Login: &member1}}, fake.GenerateEmptyResponse(), nil
								}
								return nil, fake.GenerateEmptyResponse(), nil
							},
							MockListPendingTeamInvitationsBySlug: func(ctx context.Context, org, slug string, opts *github.ListOptions) ([]*github.Invitation, *github.Response, error) {
								return nil, fake.GenerateEmptyResponse(), nil
							},
							MockListChildTeamsByParentSlug: func(ctx context.Context, org, slug string, opts *github.ListOptions) ([]*github.Team, *github.Response, error) {
								return nil, fake.GenerateEmptyResponse(), nil
							},
						},
						Organizations: &fake.MockOrganizationsClient{
							MockGetOrgMembership: func(ctx context.Context, user, org string) (*github.Membership, *github.Response, error) {
								role := "admin"
								state := "active"
								return &github.Membership{Role: &role, State: &state}, fake.GenerateEmptyResponse(), nil
							},
						},
					},
				},
			},
			args: args{
				mg: team(withSoleMember(member1, "member")),
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: true,
				},
				err:           nil,
				partialReason: reasonRoleEnforcedByOrg,
			},
		},
		"Drift_DirectMemberNotInCRWithChild": {
			fields: fields{
				github: &ghclient.Client{
					Services: &ghclient.Services{
						Teams: &fake.MockTeamsClient{
							MockGetTeamBySlug: func(ctx context.Context, org, slug string) (*github.Team, *github.Response, error) {
								return &github.Team{
									Privacy:     &teamPrivacy,
									Description: &teamDescription,
								}, nil, nil
							},
							MockListTeamMembersBySlug: func(ctx context.Context, org, slug string, opts *github.TeamListTeamMembersOptions) ([]*github.User, *github.Response, error) {
								// Parent rollup: the two CR-declared members plus a third
								// who is a *direct* parent member (NOT inherited from the
								// child, whose rollup below is empty).
								if slug == "" {
									if opts.Role == "maintainer" {
										return []*github.User{{Login: &member1}}, fake.GenerateEmptyResponse(), nil
									}
									return []*github.User{{Login: &member2}, {Login: &member3}}, fake.GenerateEmptyResponse(), nil
								}
								// Child team has no members.
								return nil, fake.GenerateEmptyResponse(), nil
							},
							MockListPendingTeamInvitationsBySlug: func(ctx context.Context, org, slug string, opts *github.ListOptions) ([]*github.Invitation, *github.Response, error) {
								return nil, fake.GenerateEmptyResponse(), nil
							},
							MockListChildTeamsByParentSlug: func(ctx context.Context, org, slug string, opts *github.ListOptions) ([]*github.Team, *github.Response, error) {
								childSlug := "child-team"
								return []*github.Team{{Slug: &childSlug}}, fake.GenerateEmptyResponse(), nil
							},
						},
					},
				},
			},
			args: args{
				mg: team(),
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: false,
				},
				err: nil,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := external{github: tc.fields.github}
			got, err := e.Observe(tc.args.ctx, tc.args.mg)
			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ne.Observe(...): -want error, +got error:\n%s\n", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.o, got); diff != "" {
				t.Errorf("\n%s\ne.Observe(...): -want, +got:\n%s\n", tc.reason, diff)
			}
			if tc.want.partialReason != "" {
				teamCR := tc.args.mg.(*v1alpha1.Team)
				var found *xpv2.Condition
				for i := range teamCR.Status.Conditions {
					if teamCR.Status.Conditions[i].Type == "TeamMembershipPartial" {
						found = &teamCR.Status.Conditions[i]
						break
					}
				}
				if found == nil {
					t.Errorf("%s: TeamMembershipPartial condition not set", tc.reason)
				} else if found.Reason != tc.want.partialReason {
					t.Errorf("%s: TeamMembershipPartial reason = %q, want %q (message=%q)", tc.reason, found.Reason, tc.want.partialReason, found.Message)
				}
			}
		})
	}
}

// TestCreate verifies the org-membership gate inside Create. PUT to team
// membership for a non-org-member would send an org-level invitation as a
// side effect (the Membership CR's job). Create must skip such users; the
// next Observe surfaces them via TeamMembershipPartial.
func TestCreate(t *testing.T) {
	type want struct {
		err      error
		addCalls int
	}

	cases := map[string]struct {
		reason  string
		members []v1alpha1.TeamMemberUser
		active  bool
		want    want
	}{
		"ActiveOrgMember_GetsPUT": {
			reason:  "An active org member declared on the team must be PUT into team membership.",
			members: []v1alpha1.TeamMemberUser{{User: member1, Role: member1Role}},
			active:  true,
			want:    want{addCalls: 1},
		},
		"NonOrgMember_SkipsPUT": {
			reason:  "A non-org-member declared on the team must NOT be PUT — PUT would send an org invite as a side effect.",
			members: []v1alpha1.TeamMemberUser{{User: member3, Role: member3Role}},
			active:  false,
			want:    want{addCalls: 0},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			addCalls := 0
			gh := &ghclient.Client{
				Services: &ghclient.Services{
					Teams: &fake.MockTeamsClient{
						MockCreateTeam: func(ctx context.Context, org string, t github.NewTeam) (*github.Team, *github.Response, error) {
							return &github.Team{}, fake.GenerateEmptyResponse(), nil
						},
						MockAddTeamMembershipBySlug: func(ctx context.Context, org, slug, user string, opts *github.TeamAddTeamMembershipOptions) (*github.Membership, *github.Response, error) {
							addCalls++
							return &github.Membership{}, fake.GenerateEmptyResponse(), nil
						},
						// Freshly created team: no existing members, pending invites, or child teams.
						MockListTeamMembersBySlug: func(ctx context.Context, org, slug string, opts *github.TeamListTeamMembersOptions) ([]*github.User, *github.Response, error) {
							return nil, fake.GenerateEmptyResponse(), nil
						},
						MockListPendingTeamInvitationsBySlug: func(ctx context.Context, org, slug string, opts *github.ListOptions) ([]*github.Invitation, *github.Response, error) {
							return nil, fake.GenerateEmptyResponse(), nil
						},
						MockListChildTeamsByParentSlug: func(ctx context.Context, org, slug string, opts *github.ListOptions) ([]*github.Team, *github.Response, error) {
							return nil, fake.GenerateEmptyResponse(), nil
						},
					},
					Organizations: &fake.MockOrganizationsClient{
						MockGetOrgMembership: func(ctx context.Context, user, org string) (*github.Membership, *github.Response, error) {
							if !tc.active {
								return nil, nil, fake.Generate404Response()
							}
							state := "active"
							return &github.Membership{State: &state}, fake.GenerateEmptyResponse(), nil
						},
					},
				},
			}

			cr := team()
			cr.Spec.ForProvider.Members = tc.members
			e := external{github: gh}
			_, err := e.Create(context.Background(), cr)
			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ne.Create(...): -want error, +got error:\n%s\n", tc.reason, diff)
			}
			if addCalls != tc.want.addCalls {
				t.Errorf("\n%s\nAddTeamMembershipBySlug calls = %d, want %d", tc.reason, addCalls, tc.want.addCalls)
			}
		})
	}
}
