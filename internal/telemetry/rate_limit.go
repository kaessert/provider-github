/*
Copyright 2024 The Crossplane Authors.

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

package telemetry

import (
	"net/http"

	"github.com/google/go-github/v90/github"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	ctrl "sigs.k8s.io/controller-runtime"
)

// RateLimitMetrics holds only the essential Prometheus metrics for GitHub rate limiting
type RateLimitMetrics struct {
	// Rate limit remaining gauge - most important metric
	rateLimitRemaining *prometheus.GaugeVec

	// Rate limit reset time gauge
	rateLimitResetTime *prometheus.GaugeVec

	// Rate limit limit gauge
	rateLimitLimit *prometheus.GaugeVec

	// Rate limit exceeded counter
	rateLimitExceededTotal *prometheus.CounterVec

	// App-unhealthy counter — incremented when a wrapped call returns no
	// HTTP response but a non-nil error (typically a token-mint 401 or a
	// network failure). Distinct from rateLimitExceededTotal because the
	// remediation is different: rotate the credential vs wait for reset.
	appUnhealthyTotal *prometheus.CounterVec

	// API-calls counter — incremented on every wrapped GitHub call
	// (success, 429, or auth failure). Carries the method label so
	// operators can attribute traffic volume to specific endpoints per
	// (org, app, installation).
	apiCallsTotal *prometheus.CounterVec

	// Picker-picks counter — incremented every time the picker selects
	// a credential. Carries a reason label (highest_remaining,
	// random_tiebreak, only_candidate) so operators can see why each
	// credential was chosen.
	picksTotal *prometheus.CounterVec

	// Repository-unreconcilable gauge, per (org, repository, dimension).
	repositoryUnreconcilable *prometheus.GaugeVec
}

// Dimensions of declared repository state GitHub may refuse to apply.
const (
	DimensionCollaborators    = "collaborators"
	DimensionBranchProtection = "branch_protection"
	DimensionArchived         = "archived"
	DimensionSettings         = "settings"
)

// labelOrganization is the metric label naming the GitHub org a call targeted.
const labelOrganization = "organization"

// labels carried by every rate-limit metric:
//   - organization: the GitHub org the call targeted.
//   - app_id: the GitHub App ID (identifies the App definition; shared across
//     all installations of the same App).
//   - app_installation_id: the GitHub App Installation ID (identifies one
//     specific installation in one org/account). Combined with app_id, this
//     uniquely names the credential.
var rateLimitLabels = []string{labelOrganization, "app_id", "app_installation_id"}

// newRateLimitMetrics constructs the metric set without registering it. Used
// directly by tests; production code goes through NewRateLimitMetrics.
func newRateLimitMetrics() *RateLimitMetrics {
	return &RateLimitMetrics{
		rateLimitRemaining: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "github_rate_limit_remaining",
				Help: "Number of API requests remaining in the current rate limit window",
			},
			rateLimitLabels,
		),
		rateLimitResetTime: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "github_rate_limit_reset_time",
				Help: "Unix timestamp when the rate limit will reset",
			},
			rateLimitLabels,
		),
		rateLimitLimit: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "github_rate_limit_limit",
				Help: "Total number of API requests allowed in the current rate limit window",
			},
			rateLimitLabels,
		),
		rateLimitExceededTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "github_rate_limit_exceeded_total",
				Help: "Total number of times GitHub rate limit was exceeded",
			},
			rateLimitLabels,
		),
		appUnhealthyTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "github_app_unhealthy_total",
				Help: "Total number of failed GitHub API calls where no HTTP response was received (typically auth or network errors). Indicates a credential that needs operator attention.",
			},
			rateLimitLabels,
		),
		apiCallsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "github_api_calls_total",
				Help: "Total number of GitHub API calls attempted, labeled by the wrapped client method. Counts every attempt including failures.",
			},
			append(rateLimitLabels, "method"),
		),
		picksTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "github_app_picker_picks_total",
				Help: "Total number of times the credential picker selected each (org, app, installation), labeled by the reason it was chosen.",
			},
			append(rateLimitLabels, "reason"),
		),
		repositoryUnreconcilable: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "github_repository_unreconcilable",
				Help: "1 while the repository has declared state GitHub will not apply, per dimension (collaborators, branch_protection, archived)",
			},
			[]string{labelOrganization, "repository", "dimension"},
		),
	}
}

// NewRateLimitMetrics creates and registers only essential Prometheus metrics for GitHub rate limiting
func NewRateLimitMetrics(_ ctrl.Manager) *RateLimitMetrics {
	m := newRateLimitMetrics()

	// Register metrics with the default Prometheus registry
	prometheus.MustRegister(m.rateLimitRemaining)
	prometheus.MustRegister(m.rateLimitResetTime)
	prometheus.MustRegister(m.rateLimitLimit)
	prometheus.MustRegister(m.rateLimitExceededTotal)
	prometheus.MustRegister(m.appUnhealthyTotal)
	prometheus.MustRegister(m.apiCallsTotal)
	prometheus.MustRegister(m.picksTotal)
	prometheus.MustRegister(m.repositoryUnreconcilable)

	return m
}

// NewForTest builds a RateLimitMetrics instance without registering it
// with the default Prometheus registry. Exported for cross-package tests
// that need to drive the metrics through real wrapper code.
func NewForTest() *RateLimitMetrics {
	return newRateLimitMetrics()
}

// RecordAPICall increments the github_api_calls_total counter for the
// given call. Counts every attempt, regardless of outcome.
func (m *RateLimitMetrics) RecordAPICall(org, appID, installationID, method string) {
	m.apiCallsTotal.WithLabelValues(org, appID, installationID, method).Inc()
}

// RecordPickerPick increments the github_app_picker_picks_total counter
// for the given credential and pick reason.
func (m *RateLimitMetrics) RecordPickerPick(org, appID, installationID, reason string) {
	m.picksTotal.WithLabelValues(org, appID, installationID, reason).Inc()
}

// APICallsCountForTest returns the current value of github_api_calls_total
// for the given labels. Exposed for cross-package test verification of
// the wrapper → telemetry wiring; production code should not need it.
func (m *RateLimitMetrics) APICallsCountForTest(org, appID, installationID, method string) float64 {
	return counterValue(m.apiCallsTotal.WithLabelValues(org, appID, installationID, method))
}

// PicksCountForTest returns the current value of
// github_app_picker_picks_total for the given labels. Same testing-only
// caveat as APICallsCountForTest.
func (m *RateLimitMetrics) PicksCountForTest(org, appID, installationID, reason string) float64 {
	return counterValue(m.picksTotal.WithLabelValues(org, appID, installationID, reason))
}

// RepositoryUnreconcilableForTest exposes the gauge vec to cross-package tests.
func (m *RateLimitMetrics) RepositoryUnreconcilableForTest() *prometheus.GaugeVec {
	return m.repositoryUnreconcilable
}

func counterValue(c prometheus.Counter) float64 {
	pb := &dto.Metric{}
	if err := c.Write(pb); err != nil {
		return 0
	}
	return pb.GetCounter().GetValue()
}

// RecordAppUnhealthy increments the github_app_unhealthy_total counter for
// the given (organization, app, installation). Call when a GitHub API call
// returns no HTTP response but an error.
func (m *RateLimitMetrics) RecordAppUnhealthy(org, appID, installationID string) {
	m.appUnhealthyTotal.WithLabelValues(org, appID, installationID).Inc()
}

// RecordRateLimitInfo records the rate-limit information from a GitHub API
// response. The org is the GitHub organization the call targeted; appID is
// the GitHub App ID; installationID is the GitHub App Installation ID. The
// (appID, installationID) pair uniquely identifies a credential.
func (m *RateLimitMetrics) RecordRateLimitInfo(resp *github.Response, org, appID, installationID string) {
	if resp == nil {
		return
	}

	// Record rate limit exceeded
	if resp.StatusCode == http.StatusTooManyRequests {
		m.rateLimitExceededTotal.WithLabelValues(org, appID, installationID).Inc()
	}

	// Record rate limit headers if available
	if resp.Rate.Limit > 0 {
		m.rateLimitLimit.WithLabelValues(org, appID, installationID).Set(float64(resp.Rate.Limit))
		m.rateLimitRemaining.WithLabelValues(org, appID, installationID).Set(float64(resp.Rate.Remaining))
		m.rateLimitResetTime.WithLabelValues(org, appID, installationID).Set(float64(resp.Rate.Reset.Unix()))
	}
}

// SetRepositoryUnreconcilable publishes 1 when GitHub won't apply the dimension's declared state, else 0.
func (m *RateLimitMetrics) SetRepositoryUnreconcilable(org, repo, dimension string, unreconcilable bool) {
	value := 0.0
	if unreconcilable {
		value = 1
	}
	m.repositoryUnreconcilable.WithLabelValues(org, repo, dimension).Set(value)
}

// ForgetRepository deletes every github_repository_unreconcilable series of the repository.
func (m *RateLimitMetrics) ForgetRepository(org, repo string) {
	m.repositoryUnreconcilable.DeletePartialMatch(prometheus.Labels{labelOrganization: org, "repository": repo})
}
