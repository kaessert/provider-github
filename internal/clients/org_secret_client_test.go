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

package clients

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-github/v90/github"

	"github.com/provider-github/provider-github/internal/clients/fake"
)

// The organization secret calls pass their arguments and results through the
// rate-limit wrapper unchanged, and each is counted under its own method label.
func TestClientOrgSecretCallsDelegateAndAreCounted(t *testing.T) {
	swapGlobalPool(t, newQuotaPool(time.Now))
	metrics := telemetryNewForTest(t)

	var gotPut github.SecretOrgRequest
	var gotOrgs, gotNames []string
	boom := errors.New("boom")
	actions := &fake.MockActionsClient{
		MockGetOrgPublicKey: func(_ context.Context, org string) (*github.PublicKey, *github.Response, error) {
			gotOrgs = append(gotOrgs, org)
			return &github.PublicKey{KeyID: github.Ptr("k1"), Key: github.Ptr("a2V5")}, &github.Response{Response: &http.Response{StatusCode: 200}}, nil
		},
		MockCreateOrUpdateOrgSecret: func(_ context.Context, org, name string, s github.SecretOrgRequest) (*github.Response, error) {
			gotOrgs, gotNames, gotPut = append(gotOrgs, org), append(gotNames, name), s
			return &github.Response{Response: &http.Response{StatusCode: 201}}, nil
		},
		MockDeleteOrgSecret: func(_ context.Context, org, name string) (*github.Response, error) {
			gotOrgs, gotNames = append(gotOrgs, org), append(gotNames, name)
			return &github.Response{Response: &http.Response{StatusCode: 500}}, boom
		},
	}
	client := NewClient(&Services{Actions: actions}, metrics).WithRateLimitTracking("acme", "12345", "67890", "k")

	pk, _, err := client.Actions.GetOrgPublicKey(context.Background(), "acme")
	if err != nil || pk.GetKeyID() != "k1" || pk.GetKey() != "a2V5" {
		t.Errorf("GetOrgPublicKey = %+v, %v; want the key the service returned", pk, err)
	}

	req := github.SecretOrgRequest{EncryptedValue: "ZW5j", KeyID: "k1", Visibility: "selected", SelectedRepositoryIDs: []int64{7, 9}}
	resp, err := client.Actions.CreateOrUpdateOrgSecret(context.Background(), "acme", "MY_SECRET", req)
	if err != nil || resp.StatusCode != 201 {
		t.Errorf("CreateOrUpdateOrgSecret = %v, %v; want the 201 the service returned", resp, err)
	}
	if diff := cmp.Diff(req, gotPut); diff != "" {
		t.Errorf("request reaching the service: -want, +got:\n%s", diff)
	}

	if _, err := client.Actions.DeleteOrgSecret(context.Background(), "acme", "MY_SECRET"); !errors.Is(err, boom) {
		t.Errorf("DeleteOrgSecret error = %v, want the service's error", err)
	}

	if diff := cmp.Diff([]string{"acme", "acme", "acme"}, gotOrgs); diff != "" {
		t.Errorf("orgs reaching the service: -want, +got:\n%s", diff)
	}
	if diff := cmp.Diff([]string{"MY_SECRET", "MY_SECRET"}, gotNames); diff != "" {
		t.Errorf("names reaching the service: -want, +got:\n%s", diff)
	}
	for _, m := range []string{"Actions.GetOrgPublicKey", "Actions.CreateOrUpdateOrgSecret", "Actions.DeleteOrgSecret"} {
		if got := telemetryAPICallsCount(metrics, "acme", "12345", "67890", m); got != 1 {
			t.Errorf("api_calls_total{method=%s} = %v, want 1", m, got)
		}
	}
}

// A 429 on the public key or on the PUT cools the credential down, like any
// other wrapped call.
func TestClientOrgSecretCallsRecordRateLimit(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	resetAt := now.Add(15 * time.Minute)
	limited := func() *github.Response {
		return &github.Response{
			Response: &http.Response{StatusCode: http.StatusTooManyRequests},
			Rate:     github.Rate{Limit: 5000, Remaining: 0, Reset: github.Timestamp{Time: resetAt}},
		}
	}
	tooMany := &github.ErrorResponse{Response: &http.Response{StatusCode: http.StatusTooManyRequests}}

	cases := map[string]func(c *Client){
		"GetOrgPublicKey": func(c *Client) { _, _, _ = c.Actions.GetOrgPublicKey(context.Background(), "acme") },
		"CreateOrUpdateOrgSecret": func(c *Client) {
			_, _ = c.Actions.CreateOrUpdateOrgSecret(context.Background(), "acme", "N", github.SecretOrgRequest{})
		},
		"DeleteOrgSecret": func(c *Client) { _, _ = c.Actions.DeleteOrgSecret(context.Background(), "acme", "N") },
	}
	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			swapGlobalPool(t, newQuotaPool(func() time.Time { return now }))
			actions := &fake.MockActionsClient{
				MockGetOrgPublicKey: func(context.Context, string) (*github.PublicKey, *github.Response, error) {
					return nil, limited(), tooMany
				},
				MockCreateOrUpdateOrgSecret: func(context.Context, string, string, github.SecretOrgRequest) (*github.Response, error) {
					return limited(), tooMany
				},
				MockDeleteOrgSecret: func(context.Context, string, string) (*github.Response, error) {
					return limited(), tooMany
				},
			}
			call(NewClient(&Services{Actions: actions}, nil).WithRateLimitTracking("acme", "app", "install", "key"))
			if q := globalPool.snapshot("key"); !q.CooldownUntil.Equal(resetAt) {
				t.Errorf("CooldownUntil = %v, want %v", q.CooldownUntil, resetAt)
			}
		})
	}
}
