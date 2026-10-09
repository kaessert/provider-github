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

package v1alpha1

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"
)

// Every namespaced example decodes strictly into the Go type of its Kind, so a
// field the type does not declare — which the API server would prune silently —
// fails here.
func TestNamespacedExamplesDecodeStrictly(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := SchemeBuilder.AddToScheme(scheme); err != nil {
		t.Fatalf("add to scheme: %v", err)
	}

	// One directory per resource under examples/; examples/provider holds the
	// ProviderConfig manifests, which are not managed resources of this API group.
	matches, err := filepath.Glob("../../../../examples/*/*-namespaced.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, file := range matches {
		if filepath.Base(filepath.Dir(file)) != "provider" {
			files = append(files, file)
		}
	}
	if len(files) != 9 {
		t.Fatalf("found %d namespaced examples, want 9", len(files))
	}

	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for i, doc := range bytes.Split(data, []byte("\n---")) {
			var meta struct {
				APIVersion string `json:"apiVersion"`
				Kind       string `json:"kind"`
			}
			if err := yaml.Unmarshal(doc, &meta); err != nil {
				t.Fatalf("%s doc %d: %v", file, i, err)
			}
			if meta.Kind == "" {
				continue
			}
			gv, err := schema.ParseGroupVersion(meta.APIVersion)
			if err != nil || gv != SchemeGroupVersion {
				continue // not one of this API's kinds (for example a Secret)
			}
			obj, err := scheme.New(gv.WithKind(meta.Kind))
			if err != nil {
				t.Errorf("%s doc %d: %v", file, i, err)
				continue
			}
			if err := yaml.UnmarshalStrict(doc, obj); err != nil {
				t.Errorf("%s doc %d (%s): %v", file, i, meta.Kind, err)
			}
		}
	}
}
