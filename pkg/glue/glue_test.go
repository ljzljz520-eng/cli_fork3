// Copyright 2023 Vic Shóstak and Create Go App Contributors. All rights reserved.
// Use of this source code is governed by Apache 2.0 license
// that can be found in the LICENSE file.

package glue

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/create-go-app/cli/v4/pkg/contract"
	"github.com/create-go-app/cli/v4/pkg/solver"
	"github.com/create-go-app/cli/v4/pkg/verify"
)

func solveForTest(t *testing.T, sel solver.Selection) *solver.Solution {
	t.Helper()
	catalog, err := contract.LoadEmbeddedCatalog()
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	sol, conflicts := solver.Solve(catalog, sel, solver.Options{})
	if len(conflicts) > 0 {
		t.Fatalf("unexpected conflicts: %v", conflicts)
	}
	return sol
}

func TestGenerate_FullFiberStackWithNginx(t *testing.T) {
	workDir := t.TempDir()
	sol := solveForTest(t, solver.Selection{
		Backend:  "fiber",
		Frontend: "react-ts",
		Database: "postgres",
		Cache:    "redis",
		Proxy:    "nginx",
	})

	result, err := Generate(sol, Options{
		WorkDir:       workDir,
		ProjectName:   "cgapp_test_full",
		ProjectDomain: "example.com",
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	// Backend env.
	backendEnv := readFile(t, filepath.Join(workDir, "backend", ".env"))
	for _, want := range []string{
		"SERVER_PORT=5000",
		"DB_HOST=cgapp-postgres",
		"DB_PORT=5432",
		"DB_SSL_MODE=disable",
		"REDIS_HOST=cgapp-redis",
		"REDIS_PORT=6379",
	} {
		if !strings.Contains(backendEnv, want) {
			t.Errorf("backend .env missing %q:\n%s", want, backendEnv)
		}
	}

	// Frontend env goes through the nginx same-origin URL.
	frontendEnv := readFile(t, filepath.Join(workDir, "frontend", ".env.local"))
	if !strings.Contains(frontendEnv, "VITE_API_BASE_URL=http://example.com/api/v1") {
		t.Errorf("frontend .env.local:\n%s", frontendEnv)
	}

	// Inventory.
	hosts := readFile(t, filepath.Join(workDir, "hosts.ini"))
	for _, want := range []string{
		"backend_port=5000",
		"postgres_container_name=cgapp-postgres",
		"redis_container_name=cgapp-redis",
		"backend_migrations_path=backend/platform/migrations",
		"nginx_version=alpine",
		"# cgapp_contract=" + contract.SchemaVersion,
	} {
		if !strings.Contains(hosts, want) {
			t.Errorf("hosts.ini missing %q", want)
		}
	}
	if strings.Contains(hosts, "traefik_version") {
		t.Errorf("hosts.ini must not contain traefik section:\n%s", hosts)
	}

	// Playbook order: redis before postgres, nginx last.
	playbook := readFile(t, filepath.Join(workDir, "playbook.yml"))
	redisPos := strings.Index(playbook, "role: redis")
	pgPos := strings.Index(playbook, "role: postgres")
	nginxPos := strings.Index(playbook, "role: nginx")
	if !(redisPos > 0 && redisPos < pgPos && pgPos < nginxPos) {
		t.Errorf("playbook role order wrong:\n%s", playbook)
	}

	// Compose.
	composePath := filepath.Join(workDir, ComposeFileName)
	composeData := readFile(t, composePath)
	var stack struct {
		Services map[string]any `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(composeData), &stack); err != nil {
		t.Fatalf("compose is not valid YAML: %v", err)
	}
	for _, service := range []string{"cgapp-postgres", "cgapp-redis", "cgapp-backend", "cgapp-nginx", "migrate"} {
		if _, ok := stack.Services[service]; !ok {
			t.Errorf("compose missing service %q; have %v", service, serviceNames(stack.Services))
		}
	}

	// Verify plan.
	plan := result.Plan
	if plan == nil {
		t.Fatal("verify plan expected")
	}
	if len(plan.DataServices) != 2 || plan.DataServices[0] != "cgapp-postgres" || plan.DataServices[1] != "cgapp-redis" {
		t.Errorf("data services: %v", plan.DataServices)
	}
	if plan.MigrateService != "migrate" {
		t.Errorf("migrate service: %q", plan.MigrateService)
	}
	probeKinds := map[string]verify.ProbeKind{}
	for _, probe := range plan.Probes {
		probeKinds[probe.Name] = probe.Kind
	}
	for _, name := range []string{"postgres-tcp", "redis-tcp", "backend-health", "nginx-tcp", "nginx-route"} {
		if _, ok := probeKinds[name]; !ok {
			t.Errorf("missing probe %q; have %v", name, probeKinds)
		}
	}
	if _, ok := probeKinds["backend-cors"]; ok {
		t.Error("CORS probe must not exist behind a same-origin proxy")
	}
	if _, err := os.Stat(filepath.Join(workDir, NginxVerifyConfFile)); err != nil {
		t.Errorf("nginx verify config: %v", err)
	}
}

func TestGenerate_MinimalChiStack(t *testing.T) {
	workDir := t.TempDir()
	sol := solveForTest(t, solver.Selection{Backend: "chi"})

	result, err := Generate(sol, Options{WorkDir: workDir, ProjectName: "cgapp_test_min"})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	hosts := readFile(t, filepath.Join(workDir, "hosts.ini"))
	for _, unwanted := range []string{"postgres_container_name", "redis_container_name", "traefik_version", "nginx_version"} {
		if strings.Contains(hosts, unwanted) {
			t.Errorf("minimal hosts.ini must not contain %q", unwanted)
		}
	}
	if !strings.Contains(hosts, "backend_port=5000") {
		t.Error("backend port missing")
	}

	playbook := readFile(t, filepath.Join(workDir, "playbook.yml"))
	for _, unwanted := range []string{"role: postgres", "role: redis", "role: traefik", "role: nginx"} {
		if strings.Contains(playbook, unwanted) {
			t.Errorf("minimal playbook must not contain %q", unwanted)
		}
	}

	plan := result.Plan
	if plan == nil || plan.MigrateService != "" || len(plan.DataServices) != 0 {
		t.Fatalf("unexpected plan: %+v", plan)
	}
	composeData := readFile(t, filepath.Join(workDir, ComposeFileName))
	var stack struct {
		Services map[string]any `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(composeData), &stack); err != nil {
		t.Fatalf("compose yaml: %v", err)
	}
	if len(stack.Services) != 1 {
		t.Errorf("minimal stack should have only backend, got %v", serviceNames(stack.Services))
	}
	foundHealth := false
	for _, probe := range plan.Probes {
		if probe.Name == "backend-health" && probe.Kind == verify.ProbeHTTP &&
			strings.HasSuffix(probe.URL, "/hc/status") && probe.ExpectStatus == 200 {
			foundHealth = true
		}
	}
	if !foundHealth {
		t.Errorf("chi health probe missing: %+v", plan.Probes)
	}
}

func TestGenerate_TraefikCORSAndFrontendDirect(t *testing.T) {
	workDir := t.TempDir()
	sol := solveForTest(t, solver.Selection{
		Backend:  "fiber",
		Frontend: "react",
		Database: "postgres",
		Cache:    "redis",
		Proxy:    "traefik",
	})

	result, err := Generate(sol, Options{WorkDir: workDir, ProjectName: "cgapp_test_traefik"})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	hosts := readFile(t, filepath.Join(workDir, "hosts.ini"))
	if !strings.Contains(hosts, "traefik_version=latest") || strings.Contains(hosts, "acme_dns_provider") {
		t.Error("hosts.ini should have plain traefik section without DNS provider")
	}

	composeData := readFile(t, filepath.Join(workDir, ComposeFileName))
	var stack struct {
		Services map[string]struct {
			Labels map[string]string `yaml:"labels"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(composeData), &stack); err != nil {
		t.Fatalf("compose yaml: %v", err)
	}
	backend, ok := stack.Services["cgapp-backend"]
	if !ok {
		t.Fatal("backend service missing")
	}
	if backend.Labels["traefik.http.routers.backend.entrypoints"] != "web" {
		t.Errorf("traefik labels: %v", backend.Labels)
	}
	if _, err := os.Stat(filepath.Join(workDir, TraefikVerifyConfFile)); err != nil {
		t.Errorf("traefik verify config: %v", err)
	}

	var hasRouteProbe bool
	for _, probe := range result.Plan.Probes {
		if probe.Name == "traefik-route" && probe.Host == "example.com" {
			hasRouteProbe = true
		}
	}
	if !hasRouteProbe {
		t.Errorf("traefik host-header route probe missing: %+v", result.Plan.Probes)
	}

	// No proxy => direct backend URL for the frontend env.
	workDir2 := t.TempDir()
	sol2 := solveForTest(t, solver.Selection{Backend: "fiber", Frontend: "react", Database: "postgres", Cache: "redis"})
	if _, err := Generate(sol2, Options{WorkDir: workDir2, ProjectName: "direct"}); err != nil {
		t.Fatalf("generate direct: %v", err)
	}
	frontendEnv := readFile(t, filepath.Join(workDir2, "frontend", ".env.local"))
	if !strings.Contains(frontendEnv, "VITE_API_BASE_URL=http://localhost:5000/api/v1") {
		t.Errorf("direct frontend env:\n%s", frontendEnv)
	}
	var stack2 struct {
		Services map[string]any `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(readFile(t, filepath.Join(workDir2, ComposeFileName))), &stack2); err != nil {
		t.Fatal(err)
	}
	if _, ok := stack2.Services["cgapp-backend"]; !ok {
		t.Fatal("backend missing")
	}
}

func TestGenerate_LooseBackendSkipsVerify(t *testing.T) {
	workDir := t.TempDir()
	sol := solveForTest(t, solver.Selection{Backend: "custom"})

	result, err := Generate(sol, Options{WorkDir: workDir, ProjectName: "loose"})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if result.Plan != nil {
		t.Errorf("loose backend must not produce a verify plan: %+v", result.Plan)
	}
	if _, err := os.Stat(filepath.Join(workDir, "playbook.yml")); err != nil {
		t.Errorf("ansible artifacts still expected: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workDir, "backend", ".env")); !os.IsNotExist(err) {
		t.Errorf("loose backend must not render .env: %v", err)
	}
}

func TestPruneRoles(t *testing.T) {
	workDir := t.TempDir()
	roles := []string{"docker", "backend", "postgres", "redis", "nginx", "traefik"}
	for _, role := range roles {
		if err := os.MkdirAll(filepath.Join(workDir, "roles", role), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	PruneRoles(workDir, "postgres", "redis", "nginx")
	pruned := []string{"traefik"}
	kept := []string{"docker", "backend", "postgres", "redis", "nginx"}
	for _, role := range pruned {
		if _, err := os.Stat(filepath.Join(workDir, "roles", role)); !os.IsNotExist(err) {
			t.Errorf("role %s should be pruned", role)
		}
	}
	for _, role := range kept {
		if _, err := os.Stat(filepath.Join(workDir, "roles", role)); err != nil {
			t.Errorf("role %s should be kept: %v", role, err)
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func serviceNames(services map[string]any) []string {
	names := make([]string, 0, len(services))
	for name := range services {
		names = append(names, name)
	}
	return names
}
