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
	"errors"
	"testing"

	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/google/go-cmp/cmp"

	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	"github.com/crossplane/crossplane-runtime/v2/pkg/test"
	"github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	ghclient "github.com/crossplane/provider-github/internal/clients"
	"github.com/crossplane/provider-github/internal/clients/fake"
	"github.com/google/go-github/v90/github"
)

var (
	org            = "testOrg"
	userName       = "testUser"
	role           = "member"
	ID       int64 = 1234
)

// Unlike many Kubernetes projects Crossplane does not use third party testing
// libraries, per the common Go test review comments. Crossplane encourages the
// use of table driven unit tests. The tests of the crossplane-runtime project
// are representative of the testing style Crossplane encourages.
//
// https://github.com/golang/go/wiki/TestComments
// https://github.com/crossplane/crossplane/blob/master/CONTRIBUTING.md#contributing-code

type membershipModifier func(*v1alpha1.Membership)

func membership(m ...membershipModifier) *v1alpha1.Membership {
	cr := &v1alpha1.Membership{}
	cr.Spec.ForProvider.Role = role
	cr.Spec.ForProvider.Org = org

	meta.SetExternalName(cr, userName)
	for _, f := range m {
		f(cr)
	}
	return cr
}

func withAdminRole() membershipModifier {
	return func(r *v1alpha1.Membership) {
		r.Spec.ForProvider.Role = "admin"
	}
}

func withInvalidMemberRole() membershipModifier {
	return func(r *v1alpha1.Membership) {
		r.Spec.ForProvider.Role = "direct_member"
	}
}

func githubMembership() *github.Membership {
	return &github.Membership{
		User: githubUser(),
		Role: &role,
	}
}

func githubUser() *github.User {
	return &github.User{
		Login: &userName,
		ID:    &ID,
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
						Organizations: &fake.MockOrganizationsClient{
							MockGetOrgMembership: func(ctx context.Context, user, org string) (*github.Membership, *github.Response, error) {
								return githubMembership(), nil, nil
							},
						},
					},
				},
			},
			args: args{
				mg: membership(),
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: true,
				},
				err: nil,
			},
		},
		"NotUpToDate": {
			fields: fields{
				github: &ghclient.Client{
					Services: &ghclient.Services{
						Organizations: &fake.MockOrganizationsClient{
							MockGetOrgMembership: func(ctx context.Context, user, org string) (*github.Membership, *github.Response, error) {
								return githubMembership(), nil, nil
							},
						},
					},
				},
			},
			args: args{
				mg: membership(withAdminRole()),
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: false,
				},
				err: nil,
			},
		},
		"DoesNotExists": {
			fields: fields{
				github: &ghclient.Client{
					Services: &ghclient.Services{
						Organizations: &fake.MockOrganizationsClient{
							MockGetOrgMembership: func(ctx context.Context, user, org string) (*github.Membership, *github.Response, error) {
								return nil, nil, fake.Generate404Response()
							},
						},
					},
				},
			},
			args: args{
				mg: &v1alpha1.Membership{},
			},
			want: want{
				o:   managed.ExternalObservation{ResourceExists: false},
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
		})
	}
}

func TestCreate(t *testing.T) {
	type fields struct {
		github *ghclient.Client
	}

	type args struct {
		ctx context.Context
		mg  resource.Managed
	}

	type want struct {
		o   managed.ExternalCreation
		err error
	}

	cases := map[string]struct {
		reason string
		fields fields
		args   args
		want   want
	}{
		"InvalidRole": {
			fields: fields{
				github: &ghclient.Client{
					Services: &ghclient.Services{
						Users: &fake.MockUsersClient{
							MockGet: func(ctx context.Context, user string) (*github.User, *github.Response, error) {
								return githubUser(), nil, nil
							},
						},
					},
				},
			},
			args: args{
				mg: membership(withInvalidMemberRole()),
			},
			want: want{
				o:   managed.ExternalCreation{},
				err: errors.New(""),
			},
		},
		"OK": {
			fields: fields{
				github: &ghclient.Client{
					Services: &ghclient.Services{
						Users: &fake.MockUsersClient{
							MockGet: func(ctx context.Context, user string) (*github.User, *github.Response, error) {
								return githubUser(), nil, nil
							},
						},
						Organizations: &fake.MockOrganizationsClient{
							MockCreateOrgInvitation: func(ctx context.Context, org string, opts *github.CreateOrgInvitationOptions) (*github.Invitation, *github.Response, error) {
								m := membership().Spec.ForProvider
								ghu := githubUser()

								// because we work around a quirk in github api wher in invitations
								// it's "direct_member" instead of member we check here directly
								if org != m.Org ||
									opts.InviteeID != ghu.ID ||
									*opts.Role != "direct_member" {

									return nil, nil, errors.New("Objects don't match")
								}

								return nil, nil, nil
							},
						},
					},
				},
			},
			args: args{
				mg: membership(),
			},
			want: want{
				o:   managed.ExternalCreation{},
				err: nil,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := external{github: tc.fields.github}
			got, err := e.Create(tc.args.ctx, tc.args.mg)
			if tc.want.err != nil && err == nil {
				t.Errorf("\ne.Create(...): -want error, +got no error.\n")
			}
			if diff := cmp.Diff(tc.want.o, got); diff != "" {
				t.Errorf("\n%s\ne.Create(...): -want, +got:\n%s\n", tc.reason, diff)
			}
		})
	}
}

func TestUpdate(t *testing.T) {
	type fields struct {
		github *ghclient.Client
	}

	type args struct {
		ctx context.Context
		mg  resource.Managed
	}

	type want struct {
		o   managed.ExternalUpdate
		err error
	}

	cases := map[string]struct {
		reason string
		fields fields
		args   args
		want   want
	}{
		"OK": {
			fields: fields{
				github: &ghclient.Client{
					Services: &ghclient.Services{
						Users: &fake.MockUsersClient{
							MockGet: func(ctx context.Context, user string) (*github.User, *github.Response, error) {
								return githubUser(), nil, nil
							},
						},
						Organizations: &fake.MockOrganizationsClient{
							MockEditOrgMembership: func(ctx context.Context, user, org string, ghm *github.Membership) (*github.Membership, *github.Response, error) {
								return nil, nil, nil
							},
						},
					},
				},
			},
			args: args{
				mg: membership(withInvalidMemberRole()),
			},
			want: want{
				o:   managed.ExternalUpdate{},
				err: nil,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := external{github: tc.fields.github}
			got, err := e.Update(tc.args.ctx, tc.args.mg)
			if tc.want.err != nil && err == nil {
				t.Errorf("\ne.Update(...): -want error, +got no error.\n")
			}
			if diff := cmp.Diff(tc.want.o, got); diff != "" {
				t.Errorf("\n%s\ne.Update(...): -want, +got:\n%s\n", tc.reason, diff)
			}
		})
	}
}
