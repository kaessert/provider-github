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

package main

import (
	"context"
	"testing"
	"time"

	authv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

// fakeManager satisfies the two ctrl.Manager methods the precheck uses.
type fakeManager struct {
	ctrl.Manager
	c client.Client
}

func (m *fakeManager) GetClient() client.Client { return m.c }

func (m *fakeManager) GetScheme() *runtime.Scheme { return clientgoscheme.Scheme }

// reviewClient answers every SelfSubjectAccessReview with allow(call), where
// call counts the reviews seen so far, or fails it with err.
func reviewClient(allow func(call int) bool, err error) client.Client {
	calls := 0
	return fake.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
		Create: func(_ context.Context, _ client.WithWatch, obj client.Object, _ ...client.CreateOption) error {
			if err != nil {
				return err
			}
			if sar, ok := obj.(*authv1.SelfSubjectAccessReview); ok {
				calls++
				sar.Status.Allowed = allow(calls)
			}
			return nil
		},
	}).Build()
}

func TestHasCRDWatchPermissions(t *testing.T) {
	boom := errors.New("boom")
	cases := map[string]struct {
		allow   func(int) bool
		err     error
		want    bool
		wantErr bool
	}{
		"AllVerbsAllowed": {allow: func(int) bool { return true }, want: true},
		"WatchDenied":     {allow: func(call int) bool { return call < 3 }, want: false},
		"APIError":        {allow: func(int) bool { return true }, err: boom, wantErr: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := hasCRDWatchPermissions(context.Background(), &fakeManager{c: reviewClient(tc.allow, tc.err)})
			if (err != nil) != tc.wantErr {
				t.Fatalf("hasCRDWatchPermissions(...): error = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("hasCRDWatchPermissions(...): got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCanWatchCRDRetriesUntilGranted(t *testing.T) {
	// Denied on the first review, granted afterwards: the precheck must keep
	// polling rather than treat the first denial as final.
	c := reviewClient(func(call int) bool { return call > 1 }, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	got, err := canWatchCRD(ctx, &fakeManager{c: c})
	if err != nil {
		t.Fatalf("canWatchCRD(...): unexpected error: %v", err)
	}
	if !got {
		t.Error("canWatchCRD(...): got false, want true once the permission is granted")
	}
}

func TestCanWatchCRDDenialAtDeadlineIsNotAnError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	got, err := canWatchCRD(ctx, &fakeManager{c: reviewClient(func(int) bool { return false }, nil)})
	if err != nil {
		t.Fatalf("canWatchCRD(...): a denial at the deadline must not be an error, got %v", err)
	}
	if got {
		t.Error("canWatchCRD(...): got true, want false for a permanent denial")
	}
}
