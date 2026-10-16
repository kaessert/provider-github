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
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"
)

// TestWrapperParity asserts that every method on every service interface
// in services.go has an explicit wrapper on the corresponding per-service
// struct in client.go.
//
// Each per-service wrapper embeds the service interface, so a missing
// wrapper compiles fine — the call falls through to the underlying
// go-github client and bypasses both github_api_calls_total recording and
// the per-credential cooldown pool's response-header capture. The
// observability gap and picker blind spot are silent, hence this parity
// test as a regression guard.
func TestWrapperParity(t *testing.T) {
	pairings := map[string]string{
		"ActionsClient":       "actionsClient",
		"DependabotClient":    "dependabotClient",
		"OrganizationsClient": "organizationsClient",
		"UsersClient":         "usersClient",
		"TeamsClient":         "teamsClient",
		"RepositoriesClient":  "repositoriesClient",
		"RulesetsClient":      "rulesetsClient",
	}

	interfaceMethods := parseInterfaceMethods(t, "services.go")
	wrapperMethods := parseStructMethods(t, "client.go")

	for ifaceName, wrapperName := range pairings {
		iface, ok := interfaceMethods[ifaceName]
		if !ok {
			t.Errorf("interface %s not found in services.go", ifaceName)
			continue
		}
		wrapper, ok := wrapperMethods[wrapperName]
		if !ok {
			t.Errorf("struct %s not found in client.go", wrapperName)
			continue
		}

		var missing []string
		for m := range iface {
			if _, ok := wrapper[m]; !ok {
				missing = append(missing, m)
			}
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			t.Errorf("%s missing %d wrapper(s) for %s: %s",
				wrapperName, len(missing), ifaceName, strings.Join(missing, ", "))
		}
	}
}

func parseInterfaceMethods(t *testing.T, file string) map[string]map[string]struct{} {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	out := map[string]map[string]struct{}{}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			it, ok := ts.Type.(*ast.InterfaceType)
			if !ok {
				continue
			}
			methods := map[string]struct{}{}
			for _, m := range it.Methods.List {
				// Embedded interfaces have no Names; skip them.
				for _, name := range m.Names {
					methods[name.Name] = struct{}{}
				}
			}
			out[ts.Name.Name] = methods
		}
	}
	return out
}

func parseStructMethods(t *testing.T, file string) map[string]map[string]struct{} {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	out := map[string]map[string]struct{}{}
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Recv == nil || len(fd.Recv.List) == 0 {
			continue
		}
		recvType := receiverTypeName(fd.Recv.List[0].Type)
		if recvType == "" {
			continue
		}
		if out[recvType] == nil {
			out[recvType] = map[string]struct{}{}
		}
		out[recvType][fd.Name.Name] = struct{}{}
	}
	return out
}

// receiverTypeName extracts "T" from a receiver expression of the form
// "*T" or "T". Returns "" for anything else.
func receiverTypeName(expr ast.Expr) string {
	if star, ok := expr.(*ast.StarExpr); ok {
		if id, ok := star.X.(*ast.Ident); ok {
			return id.Name
		}
		return ""
	}
	if id, ok := expr.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}
