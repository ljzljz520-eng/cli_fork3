// Copyright 2023 Vic Shóstak and Create Go App Contributors. All rights reserved.
// Use of this source code is governed by Apache 2.0 license
// that can be found in the LICENSE file.

package solver

import (
	"testing"

	"github.com/create-go-app/cli/v4/pkg/contract"
)

func mustCatalog(t *testing.T) *contract.Catalog {
	t.Helper()
	catalog, err := contract.LoadEmbeddedCatalog()
	if err != nil {
		t.Fatalf("LoadEmbeddedCatalog() error = %v", err)
	}
	return catalog
}

func fullSelection() Selection {
	return Selection{
		Backend:  "fiber",
		Frontend: "react-ts",
		Database: "postgres",
		Cache:    "redis",
		Proxy:    "traefik",
	}
}

func TestSolveFullStack(t *testing.T) {
	solution, conflicts := Solve(mustCatalog(t), fullSelection(), Options{})
	if len(conflicts) > 0 {
		t.Fatalf("expected no conflicts, got: %v", conflicts)
	}
	if solution.APIBasePath != "/api/v1" {
		t.Errorf("APIBasePath = %q, want /api/v1", solution.APIBasePath)
	}
	backend := solution.ByKind[contract.KindBackend]
	if backend == nil || backend.Name != "fiber" {
		t.Fatalf("backend not resolved: %+v", backend)
	}
	if got := backend.HostPorts["http"]; got != 5000 {
		t.Errorf("backend http host port = %d, want 5000", got)
	}
	if got := solution.ByKind[contract.KindDatabase].HostPorts["postgres"]; got != 5432 {
		t.Errorf("postgres host port = %d, want 5432", got)
	}
	if got := solution.ByKind[contract.KindCache].HostPorts["redis"]; got != 6379 {
		t.Errorf("redis host port = %d, want 6379", got)
	}
	if got := solution.ByKind[contract.KindProxy].HostPorts["http"]; got != 80 {
		t.Errorf("proxy http host port = %d, want 80", got)
	}
}

func TestSolveMinimalChiStack(t *testing.T) {
	// chi needs nothing: no DB, no cache, no frontend, no proxy.
	sel := Selection{Backend: "chi", Frontend: "none", Database: "none", Cache: "none", Proxy: "none"}
	solution, conflicts := Solve(mustCatalog(t), sel, Options{})
	if len(conflicts) > 0 {
		t.Fatalf("expected no conflicts, got: %v", conflicts)
	}
	if len(solution.Components) != 1 {
		t.Errorf("expected only backend, got %d components", len(solution.Components))
	}
	if solution.APIBasePath != "" {
		t.Errorf("chi publishes no API base path, got %q", solution.APIBasePath)
	}
}

func TestChiWithPostgresConflictsOnMigrations(t *testing.T) {
	sel := Selection{Backend: "chi", Database: "postgres"}
	_, conflicts := Solve(mustCatalog(t), sel, Options{})
	if len(conflicts) == 0 {
		t.Fatal("expected conflict for chi+postgres (no migrations), got none")
	}
	found := false
	for _, conflict := range conflicts {
		if conflict.Component == "postgres" && conflict.Capability == "migrations" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected postgres/migrations conflict, got: %v", conflicts)
	}
}

func TestFiberWithoutDatabaseConflicts(t *testing.T) {
	sel := Selection{Backend: "fiber", Database: "none", Cache: "none"}
	_, conflicts := Solve(mustCatalog(t), sel, Options{})
	if len(conflicts) < 2 {
		t.Fatalf("expected database+cache conflicts, got: %v", conflicts)
	}
	hasDatabase, hasCache := false, false
	for _, conflict := range conflicts {
		if conflict.Capability == "database" {
			hasDatabase = true
		}
		if conflict.Capability == "cache" {
			hasCache = true
		}
	}
	if !hasDatabase || !hasCache {
		t.Errorf("missing expected conflicts: %v", conflicts)
	}
}

func TestBrowserAPIConflictWithoutCORSOrProxy(t *testing.T) {
	// chi has no CORS and no proxy is selected; a browser frontend cannot
	// reach the API cross-origin.
	sel := Selection{Backend: "chi", Frontend: "react-ts", Database: "none", Cache: "none", Proxy: "none"}
	_, conflicts := Solve(mustCatalog(t), sel, Options{})
	if !conflictExists(conflicts, "react-ts", "browser-api") &&
		!conflictExists(conflicts, "react-ts", "http-api") {
		t.Errorf("expected browser-api/http-api conflict, got: %v", conflicts)
	}
}

func TestProxyProvidesBrowserAPI(t *testing.T) {
	// net/http has no CORS, but nginx provides same-origin browser access.
	sel := Selection{Backend: "net/http", Frontend: "vue", Database: "postgres", Cache: "none", Proxy: "nginx"}
	solution, conflicts := Solve(mustCatalog(t), sel, Options{})
	if len(conflicts) > 0 {
		t.Fatalf("expected no conflicts, got: %v", conflicts)
	}
	if solution.ByKind[contract.KindProxy].Name != "nginx" {
		t.Errorf("proxy = %q, want nginx", solution.ByKind[contract.KindProxy].Name)
	}
}

func TestPortFallbackAllocation(t *testing.T) {
	// Every preferred port is occupied; solver must pick candidates.
	opts := Options{IsPortFree: func(port int) bool {
		return port != 5000 && port != 5432 && port != 6379 && port != 80
	}}
	solution, conflicts := Solve(mustCatalog(t), fullSelection(), opts)
	if len(conflicts) > 0 {
		t.Fatalf("expected fallback allocation, got conflicts: %v", conflicts)
	}
	backend := solution.ByKind[contract.KindBackend]
	if backend.HostPorts["http"] == 5000 || backend.HostPorts["http"] == 0 {
		t.Errorf("backend port not reassigned: %d", backend.HostPorts["http"])
	}
	db := solution.ByKind[contract.KindDatabase]
	if db.HostPorts["postgres"] == 5432 || db.HostPorts["postgres"] == 0 {
		t.Errorf("postgres port not reassigned: %d", db.HostPorts["postgres"])
	}
}

func TestAllCandidatesBusyConflicts(t *testing.T) {
	opts := Options{IsPortFree: func(int) bool { return false }}
	_, conflicts := Solve(mustCatalog(t), Selection{Backend: "chi"}, opts)
	if len(conflicts) == 0 {
		t.Fatal("expected port exhaustion conflict")
	}
}

func TestUnknownSelectionConflict(t *testing.T) {
	_, conflicts := Solve(mustCatalog(t), Selection{Backend: "gin"}, Options{})
	if len(conflicts) != 1 || conflicts[0].Component != "gin" {
		t.Errorf("expected unknown backend conflict, got: %v", conflicts)
	}
}

func TestLooseCustomBackendWarns(t *testing.T) {
	sel := Selection{Backend: "custom", Database: "postgres", Cache: "none", Proxy: "none"}
	solution, conflicts := Solve(mustCatalog(t), sel, Options{})
	if len(conflicts) > 0 {
		t.Fatalf("loose mode must not hard-fail, got: %v", conflicts)
	}
	backend := solution.ByKind[contract.KindBackend]
	if !backend.Loose {
		t.Error("backend should be loose")
	}
	if len(solution.Warnings) == 0 {
		t.Error("expected a loose-mode warning")
	}
}

func TestDiscoveredCustomContractsUsed(t *testing.T) {
	// A custom backend publishing migrations + http-api + CORS should be
	// fully compatible with postgres + a browser frontend.
	custom := contract.Contract{
		APIVersion: contract.SchemaVersion,
		Kind:       contract.KindBackend,
		Name:       "my-stack",
		Version:    "1.0.0",
		Ports:      []contract.Port{{Name: "http", Port: 8080, HostPort: 8080}},
		Health:     &contract.HealthSpec{Type: "http", Path: "/live", Expect: 200},
		Offers: []contract.Capability{
			{Name: "http-api", Attributes: map[string]string{"basePath": "/api"}},
			{Name: "http-upstream"},
			{Name: "browser-api"},
			{Name: "migrations", Attributes: map[string]string{"format": "golang-migrate"}},
		},
	}
	sel := Selection{
		Backend:       "custom",
		CustomBackend: &custom,
		Frontend:      "svelte",
		Database:      "postgres",
		Cache:         "none",
		Proxy:         "none",
	}
	solution, conflicts := Solve(mustCatalog(t), sel, Options{})
	if len(conflicts) > 0 {
		t.Fatalf("custom contract stack should solve, got: %v", conflicts)
	}
	if solution.APIBasePath != "/api" {
		t.Errorf("base path = %q, want /api", solution.APIBasePath)
	}
}

func TestSatisfiesVersion(t *testing.T) {
	cases := []struct {
		got, want string
		expect    bool
	}{
		{"1.2.3", ">=1.0.0", true},
		{"1.2.3", ">=1.5.0", false},
		{"2.0.0", "^2.0.0", true},
		{"3.0.0", "^2.0.0", false},
		{"1.2.5", "~1.2.0", true},
		{"1.3.0", "~1.2.0", false},
		{"latest", ">=1.0.0", true}, // non-semantic tags never block
		{"1.2.3", "", true},
	}
	for _, tc := range cases {
		if got := satisfiesVersion(tc.got, tc.want); got != tc.expect {
			t.Errorf("satisfiesVersion(%q, %q) = %v, want %v", tc.got, tc.want, got, tc.expect)
		}
	}
}

func conflictExists(conflicts []Conflict, component, capability string) bool {
	for _, conflict := range conflicts {
		if conflict.Component == component && conflict.Capability == capability {
			return true
		}
	}
	return false
}
