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

// Package rulesets reads and writes repository rulesets with the provider's own
// types over go-github's transport. Each rule keeps its type and raw parameters,
// so the provider sees every rule type GitHub returns.
package rulesets

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/google/go-github/v90/github"
)

// Ruleset is a repository ruleset as the REST API returns and accepts it. Source is
// set by GitHub, so requests leave it out.
type Ruleset struct {
	ID           *int64         `json:"id,omitempty"`
	Name         string         `json:"name"`
	Target       *string        `json:"target,omitempty"`
	SourceType   *string        `json:"source_type,omitempty"`
	Source       string         `json:"source,omitempty"`
	Enforcement  string         `json:"enforcement"`
	BypassActors []*BypassActor `json:"bypass_actors,omitzero"`
	Conditions   *Conditions    `json:"conditions,omitempty"`
	Rules        []*Rule        `json:"rules,omitzero"`
}

// BypassActor is an actor that may bypass a ruleset. GitHub returns a null
// actor_id for OrganizationAdmin and DeployKey.
type BypassActor struct {
	ActorID    *int64  `json:"actor_id,omitempty"`
	ActorType  *string `json:"actor_type,omitempty"`
	BypassMode *string `json:"bypass_mode,omitempty"`
}

// Conditions holds the ref conditions of a ruleset. An empty Conditions is sent as {}
// and clears them on update. A nil Conditions keeps them.
type Conditions struct {
	RefName *RefName `json:"ref_name,omitempty"`
}

// RefName holds the ref patterns a ruleset includes and excludes.
type RefName struct {
	Include []string `json:"include"`
	Exclude []string `json:"exclude"`
}

// Rule is one entry of a ruleset's rules array, of any type.
type Rule struct {
	Type       string          `json:"type"`
	Parameters json.RawMessage `json:"parameters,omitempty"`
}

// ModelledRules holds the rules the provider models: one field per rule type, tagged
// with the type and in the order the rules are sent. A set field means the ruleset
// holds that rule.
type ModelledRules struct {
	Creation                             *EmptyRuleParameters                                `rule:"creation"`
	Update                               *UpdateRuleParameters                               `rule:"update"`
	Deletion                             *EmptyRuleParameters                                `rule:"deletion"`
	RequiredLinearHistory                *EmptyRuleParameters                                `rule:"required_linear_history"`
	MergeQueue                           *MergeQueueRuleParameters                           `rule:"merge_queue"`
	RequiredDeployments                  *RequiredDeploymentsRuleParameters                  `rule:"required_deployments"`
	RequiredSignatures                   *EmptyRuleParameters                                `rule:"required_signatures"`
	PullRequest                          *PullRequestRuleParameters                          `rule:"pull_request"`
	RequiredStatusChecks                 *RequiredStatusChecksRuleParameters                 `rule:"required_status_checks"`
	NonFastForward                       *EmptyRuleParameters                                `rule:"non_fast_forward"`
	CommitMessagePattern                 *PatternRuleParameters                              `rule:"commit_message_pattern"`
	CommitAuthorEmailPattern             *PatternRuleParameters                              `rule:"commit_author_email_pattern"`
	CommitterEmailPattern                *PatternRuleParameters                              `rule:"committer_email_pattern"`
	BranchNamePattern                    *PatternRuleParameters                              `rule:"branch_name_pattern"`
	TagNamePattern                       *PatternRuleParameters                              `rule:"tag_name_pattern"`
	FilePathRestriction                  *FilePathRestrictionRuleParameters                  `rule:"file_path_restriction"`
	MaxFilePathLength                    *MaxFilePathLengthRuleParameters                    `rule:"max_file_path_length"`
	FileExtensionRestriction             *FileExtensionRestrictionRuleParameters             `rule:"file_extension_restriction"`
	MaxFileSize                          *MaxFileSizeRuleParameters                          `rule:"max_file_size"`
	CodeScanning                         *CodeScanningRuleParameters                         `rule:"code_scanning"`
	CodeQuality                          *CodeQualityRuleParameters                          `rule:"code_quality"`
	CodeCoverage                         *CodeCoverageRuleParameters                         `rule:"code_coverage"`
	CopilotCodeReview                    *CopilotCodeReviewRuleParameters                    `rule:"copilot_code_review"`
	LicenseComplianceScanning            *EmptyRuleParameters                                `rule:"license_compliance_scanning"`
	RequireSecretScanningAlertResolution *RequireSecretScanningAlertResolutionRuleParameters `rule:"require_secret_scanning_alert_resolution"`
}

// EmptyRuleParameters marks a rule that is sent without parameters.
type EmptyRuleParameters struct{}

// UpdateRuleParameters are the parameters of the update rule. GitHub returns
// update_allows_fetch_and_merge on forks only, so it may be nil.
type UpdateRuleParameters struct {
	UpdateAllowsFetchAndMerge *bool `json:"update_allows_fetch_and_merge,omitempty"`
}

// RequireSecretScanningAlertResolutionRuleParameters are the parameters of the
// require_secret_scanning_alert_resolution rule.
type RequireSecretScanningAlertResolutionRuleParameters struct {
	SecretTypes []string `json:"secret_types"`
}

// MergeQueueRuleParameters are the modelled parameters of the merge_queue rule.
type MergeQueueRuleParameters struct {
	CheckResponseTimeoutMinutes  int    `json:"check_response_timeout_minutes"`
	GroupingStrategy             string `json:"grouping_strategy"`
	MaxEntriesToBuild            int    `json:"max_entries_to_build"`
	MaxEntriesToMerge            int    `json:"max_entries_to_merge"`
	MergeMethod                  string `json:"merge_method"`
	MinEntriesToMerge            int    `json:"min_entries_to_merge"`
	MinEntriesToMergeWaitMinutes int    `json:"min_entries_to_merge_wait_minutes"`
}

// RequiredDeploymentsRuleParameters are the modelled parameters of the
// required_deployments rule.
type RequiredDeploymentsRuleParameters struct {
	RequiredDeploymentEnvironments []string `json:"required_deployment_environments"`
}

// PullRequestRuleParameters are the modelled parameters of the pull_request rule.
// RequireExtraApprovalForUnattributedChanges is undocumented; it is sent only when set.
type PullRequestRuleParameters struct {
	AllowedMergeMethods                        []string              `json:"allowed_merge_methods"`
	DismissStaleReviewsOnPush                  bool                  `json:"dismiss_stale_reviews_on_push"`
	DismissalRestriction                       *DismissalRestriction `json:"dismissal_restriction,omitempty"`
	RequireCodeOwnerReview                     bool                  `json:"require_code_owner_review"`
	RequireExtraApprovalForUnattributedChanges *bool                 `json:"require_extra_approval_for_unattributed_changes,omitempty"`
	RequireLastPushApproval                    bool                  `json:"require_last_push_approval"`
	RequiredApprovingReviewCount               int                   `json:"required_approving_review_count"`
	RequiredReviewThreadResolution             bool                  `json:"required_review_thread_resolution"`
	RequiredReviewers                          []*RequiredReviewer   `json:"required_reviewers"`
}

// DismissalRestriction limits who may dismiss reviews under the pull_request rule.
type DismissalRestriction struct {
	AllowedActors []*Actor `json:"allowed_actors"`
	Enabled       bool     `json:"enabled"`
}

// RequiredReviewer is one team whose approval the pull_request rule requires for
// changes to matching files.
type RequiredReviewer struct {
	FilePatterns     []string `json:"file_patterns"`
	MinimumApprovals int      `json:"minimum_approvals"`
	Reviewer         Actor    `json:"reviewer"`
}

// Actor is an actor named by ID and type inside a rule's parameters. GitHub may
// return the ID as a JSON string or a number; it is sent as a number.
type Actor struct {
	ID   json.Number `json:"id"`
	Type string      `json:"type"`
}

// RequiredStatusChecksRuleParameters are the modelled parameters of the
// required_status_checks rule.
type RequiredStatusChecksRuleParameters struct {
	DoNotEnforceOnCreate             bool           `json:"do_not_enforce_on_create"`
	RequiredStatusChecks             []*StatusCheck `json:"required_status_checks"`
	StrictRequiredStatusChecksPolicy bool           `json:"strict_required_status_checks_policy"`
}

// StatusCheck is one status check of the required_status_checks rule.
type StatusCheck struct {
	Context       string `json:"context"`
	IntegrationID *int64 `json:"integration_id,omitempty"`
}

// PatternRuleParameters are the parameters of the five pattern rules.
type PatternRuleParameters struct {
	Name     *string `json:"name,omitempty"`
	Negate   *bool   `json:"negate,omitempty"`
	Operator string  `json:"operator"`
	Pattern  string  `json:"pattern"`
}

// FilePathRestrictionRuleParameters are the parameters of the file_path_restriction rule.
type FilePathRestrictionRuleParameters struct {
	IgnoredFilePaths    []string `json:"ignored_file_paths"`
	RestrictedFilePaths []string `json:"restricted_file_paths"`
}

// MaxFilePathLengthRuleParameters are the parameters of the max_file_path_length rule.
type MaxFilePathLengthRuleParameters struct {
	MaxFilePathLength int `json:"max_file_path_length"`
}

// FileExtensionRestrictionRuleParameters are the parameters of the
// file_extension_restriction rule.
type FileExtensionRestrictionRuleParameters struct {
	RestrictedFileExtensions []string `json:"restricted_file_extensions"`
}

// MaxFileSizeRuleParameters are the parameters of the max_file_size rule.
type MaxFileSizeRuleParameters struct {
	IgnoredFilePaths []string `json:"ignored_file_paths"`
	MaxFileSize      int64    `json:"max_file_size"`
}

// CodeScanningRuleParameters are the parameters of the code_scanning rule.
type CodeScanningRuleParameters struct {
	CodeScanningTools []*CodeScanningTool `json:"code_scanning_tools"`
}

// CodeScanningTool is one tool of the code_scanning rule.
type CodeScanningTool struct {
	AlertsThreshold         string `json:"alerts_threshold"`
	SecurityAlertsThreshold string `json:"security_alerts_threshold"`
	Tool                    string `json:"tool"`
}

// CodeQualityRuleParameters are the parameters of the code_quality rule.
type CodeQualityRuleParameters struct {
	Severity string `json:"severity"`
}

// CodeCoverageRuleParameters are the parameters of the code_coverage rule, in percent.
// A nil parameter is omitted from the request.
type CodeCoverageRuleParameters struct {
	MaxCoverageDrop *float64 `json:"max_coverage_drop,omitempty"`
	MinimumCoverage *float64 `json:"minimum_coverage,omitempty"`
}

// CopilotCodeReviewRuleParameters are the parameters of the copilot_code_review rule.
type CopilotCodeReviewRuleParameters struct {
	ReviewOnPush            bool `json:"review_on_push"`
	ReviewDraftPullRequests bool `json:"review_draft_pull_requests"`
}

var (
	modelledRulesType   = reflect.TypeFor[ModelledRules]()
	emptyParametersType = reflect.TypeFor[EmptyRuleParameters]()

	// modelledRuleFields maps each modelled rule type to its ModelledRules field.
	modelledRuleFields = func() map[string]int {
		fields := make(map[string]int, modelledRulesType.NumField())
		for i := range modelledRulesType.NumField() {
			fields[modelledRulesType.Field(i).Tag.Get("rule")] = i
		}
		return fields
	}()
)

// IsModelled reports whether the provider models rules of ruleType.
func IsModelled(ruleType string) bool {
	_, ok := modelledRuleFields[ruleType]
	return ok
}

// UnmanagedParameters returns, sorted, the key paths of rule's parameters, at any
// depth, that the rule type's parameter structs have no JSON field for. A path joins
// keys with dots and marks an array element with [], as in required_reviewers[].key.
// It returns nil for a rule type the provider does not model and for parameters that
// are not a JSON object.
func UnmanagedParameters(rule *Rule) []string {
	i, ok := modelledRuleFields[rule.Type]
	if !ok || len(rule.Parameters) == 0 {
		return nil
	}
	paths := map[string]bool{}
	unmanagedPaths(rule.Parameters, modelledRulesType.Field(i).Type, "", paths)
	return slices.Sorted(maps.Keys(paths))
}

// unmanagedPaths adds to paths the key paths in raw, under prefix, that t has no JSON
// field for. It walks the struct tags of t, so a known key counts as managed whatever
// value GitHub returns for it. A map, an interface or a scalar ends the walk, and a
// JSON null or a value of another shape adds nothing.
func unmanagedPaths(raw json.RawMessage, t reflect.Type, prefix string, paths map[string]bool) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Slice, reflect.Array:
		var elements []json.RawMessage
		if json.Unmarshal(raw, &elements) != nil {
			return
		}
		for _, element := range elements {
			unmanagedPaths(element, t.Elem(), prefix+"[]", paths)
		}
	case reflect.Struct:
		var object map[string]json.RawMessage
		if json.Unmarshal(raw, &object) != nil {
			return
		}
		fields := map[string]reflect.Type{}
		for j := range t.NumField() {
			field := t.Field(j)
			name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			if field.IsExported() && name != "-" {
				fields[name] = field.Type
			}
		}
		for key, value := range object {
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			if fieldType, ok := fields[key]; ok {
				unmanagedPaths(value, fieldType, path, paths)
			} else {
				paths[path] = true
			}
		}
	}
}

// Decode returns the rules the provider models, with their modelled parameters.
func Decode(rules []*Rule) (*ModelledRules, error) {
	m := &ModelledRules{}
	v := reflect.ValueOf(m).Elem()
	for _, rule := range rules {
		i, ok := modelledRuleFields[rule.Type]
		if !ok {
			continue
		}
		params := reflect.New(modelledRulesType.Field(i).Type.Elem())
		if params.Elem().Type() != emptyParametersType && len(rule.Parameters) > 0 {
			if err := json.Unmarshal(rule.Parameters, params.Interface()); err != nil {
				return nil, errors.Wrapf(err, "rule %s", rule.Type)
			}
		}
		v.Field(i).Set(params)
	}
	return m, nil
}

// Encode returns the rules array for m. With no rules it returns an empty array,
// which is sent as [] and clears the rules on GitHub.
func (m *ModelledRules) Encode() ([]*Rule, error) {
	rules := make([]*Rule, 0, modelledRulesType.NumField())
	v := reflect.ValueOf(m).Elem()
	for i := range modelledRulesType.NumField() {
		field := v.Field(i)
		if field.IsNil() {
			continue
		}
		rule := &Rule{Type: modelledRulesType.Field(i).Tag.Get("rule")}
		if field.Type().Elem() != emptyParametersType {
			params, err := json.Marshal(field.Interface())
			if err != nil {
				return nil, errors.Wrapf(err, "rule %s", rule.Type)
			}
			rule.Parameters = params
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

// Service calls the repository ruleset endpoints on go-github's transport, so the
// calls keep its authentication and rate-limit handling.
type Service struct {
	client *github.Client
}

// NewService returns a Service that sends its requests with client.
func NewService(client *github.Client) *Service {
	return &Service{client: client}
}

func (s *Service) do(ctx context.Context, method, u string, body, v any) (*github.Response, error) {
	req, err := s.client.NewRequest(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	return s.client.Do(req, v)
}

// GetAllRulesets lists the rulesets of a repository, one page per call.
func (s *Service) GetAllRulesets(ctx context.Context, owner, repo string, opts *github.RepositoryListRulesetsOptions) ([]*Ruleset, *github.Response, error) {
	u := fmt.Sprintf("repos/%v/%v/rulesets", owner, repo)
	if q := listQuery(opts); q != "" {
		u += "?" + q
	}
	var rulesets []*Ruleset
	resp, err := s.do(ctx, http.MethodGet, u, nil, &rulesets)
	if err != nil {
		return nil, resp, err
	}
	return rulesets, resp, nil
}

// listQuery encodes opts the way go-github encodes them.
func listQuery(opts *github.RepositoryListRulesetsOptions) string {
	if opts == nil {
		return ""
	}
	q := url.Values{}
	if opts.IncludesParents != nil {
		q.Set("includes_parents", strconv.FormatBool(*opts.IncludesParents))
	}
	if opts.Page != 0 {
		q.Set("page", strconv.Itoa(opts.Page))
	}
	if opts.PerPage != 0 {
		q.Set("per_page", strconv.Itoa(opts.PerPage))
	}
	return q.Encode()
}

// GetRuleset gets one ruleset of a repository, with its rules.
func (s *Service) GetRuleset(ctx context.Context, owner, repo string, rulesetID int64, includesParents bool) (*Ruleset, *github.Response, error) {
	u := fmt.Sprintf("repos/%v/%v/rulesets/%v?includes_parents=%v", owner, repo, rulesetID, includesParents)
	var ruleset *Ruleset
	resp, err := s.do(ctx, http.MethodGet, u, nil, &ruleset)
	if err != nil {
		return nil, resp, err
	}
	return ruleset, resp, nil
}

// CreateRuleset creates a ruleset on a repository.
func (s *Service) CreateRuleset(ctx context.Context, owner, repo string, ruleset Ruleset) (*Ruleset, *github.Response, error) {
	u := fmt.Sprintf("repos/%v/%v/rulesets", owner, repo)
	var created *Ruleset
	resp, err := s.do(ctx, http.MethodPost, u, ruleset, &created)
	if err != nil {
		return nil, resp, err
	}
	return created, resp, nil
}

// UpdateRuleset replaces a ruleset of a repository.
func (s *Service) UpdateRuleset(ctx context.Context, owner, repo string, rulesetID int64, ruleset Ruleset) (*Ruleset, *github.Response, error) {
	u := fmt.Sprintf("repos/%v/%v/rulesets/%v", owner, repo, rulesetID)
	var updated *Ruleset
	resp, err := s.do(ctx, http.MethodPut, u, ruleset, &updated)
	if err != nil {
		return nil, resp, err
	}
	return updated, resp, nil
}

// DeleteRuleset deletes a ruleset of a repository.
func (s *Service) DeleteRuleset(ctx context.Context, owner, repo string, rulesetID int64) (*github.Response, error) {
	u := fmt.Sprintf("repos/%v/%v/rulesets/%v", owner, repo, rulesetID)
	return s.do(ctx, http.MethodDelete, u, nil, nil)
}
