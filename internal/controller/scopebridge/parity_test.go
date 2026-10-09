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

package scopebridge

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"sigs.k8s.io/yaml"
)

const (
	crdDir          = "../../../package/crds"
	clusterGroup    = "organizations.github.crossplane.io_"
	namespacedGroup = "organizations.github.m.crossplane.io_"
)

type jsonSchema = map[string]any

func loadSchema(t *testing.T, path string) jsonSchema {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var crd jsonSchema
	if err := yaml.Unmarshal(data, &crd); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return crd
}

// at walks keys down a decoded schema and fails the test if a key is absent.
func at(t *testing.T, s jsonSchema, path string, keys ...string) jsonSchema {
	t.Helper()
	cur := s
	for _, k := range keys {
		next, ok := cur[k].(map[string]any)
		if !ok {
			t.Fatalf("%s: no %q under %v", path, k, keys)
		}
		cur = next
	}
	return cur
}

func versionSchema(t *testing.T, path string) jsonSchema {
	t.Helper()
	crd := loadSchema(t, path)
	versions, ok := at(t, crd, path, "spec")["versions"].([]any)
	if !ok || len(versions) != 1 {
		t.Fatalf("%s: want exactly one version", path)
	}
	v, ok := versions[0].(map[string]any)
	if !ok {
		t.Fatalf("%s: malformed version", path)
	}
	return at(t, v, path, "schema", "openAPIV3Schema", "properties")
}

// normalize drops what may legitimately differ between scopes: descriptions,
// and the reference and selector fields, whose schemas differ by the
// namespace a namespaced reference carries. Reference fields are kept as
// presence-only markers.
func normalize(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, val := range x {
			if k == "description" {
				if _, isSchema := val.(string); isSchema {
					continue
				}
			}
			if props, ok := val.(map[string]any); ok && k == "properties" {
				out[k] = normalizeProperties(props)
				continue
			}
			out[k] = normalize(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = normalize(x[i])
		}
		return out
	default:
		return v
	}
}

func normalizeProperties(props map[string]any) map[string]any {
	out := map[string]any{}
	for name, p := range props {
		if isReferenceField(name) {
			out[name] = "reference"
			continue
		}
		out[name] = normalize(p)
	}
	return out
}

func isReferenceField(name string) bool {
	return strings.HasSuffix(name, "Ref") || strings.HasSuffix(name, "Refs") || strings.HasSuffix(name, "Selector")
}

// The namespaced and cluster-scoped variants of a Kind share one external
// client, so their forProvider and atProvider schemas — field names, types,
// enums, required lists and CEL rules — must stay identical.
func TestScopesShareForProviderAndAtProviderSchema(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(crdDir, clusterGroup+"*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	if len(files) != 9 {
		t.Fatalf("found %d cluster-scoped Kinds, want 9", len(files))
	}

	for _, clusterFile := range files {
		plural := strings.TrimPrefix(filepath.Base(clusterFile), clusterGroup)
		t.Run(strings.TrimSuffix(plural, ".yaml"), func(t *testing.T) {
			nsFile := filepath.Join(crdDir, namespacedGroup+plural)
			cluster, namespaced := versionSchema(t, clusterFile), versionSchema(t, nsFile)

			for _, section := range [][]string{
				{"spec", "properties", "forProvider"},
				{"status", "properties", "atProvider"},
			} {
				want := normalize(at(t, cluster, clusterFile, section...))
				got := normalize(at(t, namespaced, nsFile, section...))
				if diff := cmp.Diff(want, got); diff != "" {
					t.Errorf("%s differs between scopes (-cluster +namespaced):\n%s", strings.Join(section, "."), diff)
				}
			}
		})
	}
}
