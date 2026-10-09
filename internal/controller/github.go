/*
Copyright 2020 The Crossplane Authors.

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

package controller

import (
	"time"

	"github.com/crossplane/crossplane-runtime/v2/pkg/controller"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/crossplane/provider-github/internal/controller/actionssecretaccess"
	"github.com/crossplane/provider-github/internal/controller/config"
	"github.com/crossplane/provider-github/internal/controller/dependabotsecretaccess"
	"github.com/crossplane/provider-github/internal/controller/membership"
	"github.com/crossplane/provider-github/internal/controller/organization"
	"github.com/crossplane/provider-github/internal/controller/organizationvariable"
	"github.com/crossplane/provider-github/internal/controller/organizationwebhook"
	"github.com/crossplane/provider-github/internal/controller/repository"
	"github.com/crossplane/provider-github/internal/controller/runnergroup"
	"github.com/crossplane/provider-github/internal/controller/team"
	"github.com/crossplane/provider-github/internal/telemetry"
)

// Setup creates all GitHub controllers with the supplied logger and adds them to
// the supplied manager.
func Setup(mgr ctrl.Manager, o controller.Options, metrics *telemetry.RateLimitMetrics) error {
	for _, setup := range []func(ctrl.Manager, controller.Options, *telemetry.RateLimitMetrics) error{
		config.Setup,
		organization.Setup,
		repository.Setup,
		membership.Setup,
		team.Setup,
		organizationvariable.Setup,
		runnergroup.Setup,
		organizationwebhook.Setup,
		actionssecretaccess.Setup,
		dependabotsecretaccess.Setup,
	} {
		if err := setup(mgr, o, metrics); err != nil {
			return err
		}
	}
	return nil
}

// SetupWithTimeout creates all GitHub controllers with configurable reconcile timeout
func SetupWithTimeout(mgr ctrl.Manager, o controller.Options, metrics *telemetry.RateLimitMetrics, reconcileTimeout time.Duration) error {
	// Setup all controllers with timeout
	setupFuncs := []func(ctrl.Manager, controller.Options, *telemetry.RateLimitMetrics, time.Duration) error{
		organization.SetupWithTimeout,
		repository.SetupWithTimeout,
		membership.SetupWithTimeout,
		team.SetupWithTimeout,
		organizationvariable.SetupWithTimeout,
		runnergroup.SetupWithTimeout,
		organizationwebhook.SetupWithTimeout,
		actionssecretaccess.SetupWithTimeout,
		dependabotsecretaccess.SetupWithTimeout,
	}

	for _, setup := range setupFuncs {
		if err := setup(mgr, o, metrics, reconcileTimeout); err != nil {
			return err
		}
	}

	// Setup config controller (no timeout needed for config)
	return config.Setup(mgr, o, metrics)
}
