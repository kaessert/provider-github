/*
Copyright 2026 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package util

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	pointer "k8s.io/utils/ptr"

	"github.com/provider-github/provider-github/apis/cluster/organizations/v1alpha1"
)

// Bypass actors sort into one order from any input order, nil actor IDs included.
func TestSortRulesBypassActorsNilActorID(t *testing.T) {
	actor := func(id *int64, typ, mode string) *v1alpha1.RulesetByPassActors {
		return &v1alpha1.RulesetByPassActors{ActorID: id, ActorType: pointer.To(typ), BypassMode: pointer.To(mode)}
	}
	want := []*v1alpha1.RulesetByPassActors{
		actor(nil, "DeployKey", "always"),
		actor(nil, "OrganizationAdmin", "always"),
		actor(pointer.To(int64(5)), "RepositoryRole", "always"),
		actor(pointer.To(int64(2)), "Team", "always"),
		actor(pointer.To(int64(2)), "Team", "pull_request"),
		actor(pointer.To(int64(9)), "Team", "always"),
	}
	got := []*v1alpha1.RulesetByPassActors{want[4], want[2], want[1], want[5], want[0], want[3]}

	SortRulesBypassActors(got)

	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("SortRulesBypassActors: -want, +got:\n%s", diff)
	}
}

// Status checks with the same context sort by integration ID, nil first.
func TestSortRulesRequiredStatusChecksSameContext(t *testing.T) {
	check := func(id *int64) *v1alpha1.RulesRequiredStatusChecksParameters {
		return &v1alpha1.RulesRequiredStatusChecksParameters{Context: "ci", IntegrationID: id}
	}
	want := []*v1alpha1.RulesRequiredStatusChecksParameters{check(nil), check(pointer.To(int64(1))), check(pointer.To(int64(2)))}
	got := []*v1alpha1.RulesRequiredStatusChecksParameters{want[2], want[0], want[1]}

	SortRulesRequiredStatusChecks(got)

	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("SortRulesRequiredStatusChecks: -want, +got:\n%s", diff)
	}
}

// Code scanning entries for the same tool sort into one order from any input order, so
// the CR and GitHub's copy compare equal whatever order each lists them in.
func TestSortRulesCodeScanningToolsSameTool(t *testing.T) {
	tool := func(alerts, securityAlerts string) *v1alpha1.RulesCodeScanningTool {
		return &v1alpha1.RulesCodeScanningTool{Tool: "CodeQL", AlertsThreshold: alerts, SecurityAlertsThreshold: securityAlerts}
	}
	cr := []*v1alpha1.RulesCodeScanningTool{tool("errors", "all"), tool("all", "critical"), tool("all", "all")}
	gh := []*v1alpha1.RulesCodeScanningTool{tool("all", "all"), tool("all", "critical"), tool("errors", "all")}

	SortRulesCodeScanningTools(cr)
	SortRulesCodeScanningTools(gh)

	if diff := cmp.Diff(cr, gh); diff != "" {
		t.Errorf("SortRulesCodeScanningTools: CR and GitHub orders differ: -cr, +gh:\n%s", diff)
	}
}
