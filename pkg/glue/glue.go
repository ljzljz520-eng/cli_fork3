// Copyright 2023 Vic Shóstak and Create Go App Contributors. All rights reserved.
// Use of this source code is governed by Apache 2.0 license
// that can be found in the LICENSE file.

// Package glue turns a solver solution into concrete project connection
// files: backend/frontend env, Ansible inventory and playbook, and the
// ephemeral docker-compose verification stack with its contract probes.
package glue

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/create-go-app/cli/v4/pkg/contract"
	"github.com/create-go-app/cli/v4/pkg/solver"
	"github.com/create-go-app/cli/v4/pkg/verify"
)

// ComposeFileName is the ephemeral Compose file (removed before project
// promotion).
const ComposeFileName = "docker-compose.verify.yml"

// Options controls glue generation.
type Options struct {
	// WorkDir is the temporary project root.
	WorkDir string
	// ProjectName is the Compose project name.
	ProjectName string
	// ProjectDomain is used for inventory and Host-header routing probes.
	ProjectDomain string
	// RandomSecret supplies ephemeral credentials. nil uses a built-in one.
	RandomSecret func() string
}

// Result is the generated artifact set.
type Result struct {
	// Plan describes the ephemeral verification run; nil when not generated.
	Plan *verify.Plan
	// Inventory is the rendered inventory data (for tests/inspection).
	Inventory InventoryData
	// Files written, relative to WorkDir.
	Files []string
}

// Generate writes every connection artifact derived from the solution.
func Generate(sol *solver.Solution, opts Options) (*Result, error) {
	if opts.RandomSecret == nil {
		opts.RandomSecret = randomSecret
	}
	if opts.ProjectDomain == "" {
		opts.ProjectDomain = "example.com"
	}

	result := &Result{}
	bindings := newBindings(sol)

	// Backend env file.
	if files, err := writeBackendEnv(opts.WorkDir, sol, bindings); err != nil {
		return nil, err
	} else {
		result.Files = append(result.Files, files...)
	}

	// Frontend env file.
	if files, err := writeFrontendEnv(opts.WorkDir, sol, bindings, opts.ProjectDomain); err != nil {
		return nil, err
	} else {
		result.Files = append(result.Files, files...)
	}

	// Ansible playbook + inventory.
	playbookPath, inventory, err := renderAnsible(opts.WorkDir, sol, bindings, opts.ProjectDomain)
	if err != nil {
		return nil, err
	}
	result.Inventory = inventory
	result.Files = append(result.Files, playbookPath, "hosts.ini")

	// Ephemeral verify stack.
	plan, extraFiles, err := buildVerifyStack(sol, opts, bindings)
	if err != nil {
		return nil, err
	}
	result.Plan = plan
	result.Files = append(result.Files, extraFiles...)

	return result, nil
}

// PruneRoles removes Ansible roles that are not part of the selected stack.
func PruneRoles(workDir, database, cache, proxy string) {
	remove := map[string]bool{}
	if database == "none" {
		remove["postgres"] = true
	}
	if cache == "none" {
		remove["redis"] = true
	}
	switch proxy {
	case "traefik", "traefik-acme-dns":
		remove["nginx"] = true
	case "nginx":
		remove["traefik"] = true
	default:
		remove["traefik"] = true
		remove["nginx"] = true
	}
	for role := range remove {
		_ = os.RemoveAll(filepath.Join(workDir, "roles", role))
	}
}

// bindings holds resolved connection values.
type bindings struct {
	// deploy-style values (match hosts.ini defaults, users edit them later)
	dbHost, dbPort, dbUser, dbPassword, dbName, dbSSLMode string
	cacheHost, cachePort, cachePassword, cacheDB          string
	backendHTTPPort                                       int
	// ephemeral verify credentials
	verifyDBPassword string
}

func newBindings(sol *solver.Solution) *bindings {
	b := &bindings{}
	if backend := sol.ByKind[contract.KindBackend]; backend != nil && !backend.Loose {
		if port, ok := backend.FindPort("http"); ok {
			b.backendHTTPPort = port.Port
		}
	}
	if db := sol.ByKind[contract.KindDatabase]; db != nil && !db.Loose {
		b.dbHost = producedDefault(db, "DB_HOST")
		b.dbPort = strconv.Itoa(containerPort(db, "postgres"))
		b.dbUser = producedDefault(db, "DB_USER")
		b.dbPassword = producedDefault(db, "DB_PASSWORD")
		b.dbName = producedDefault(db, "DB_NAME")
		b.dbSSLMode = producedDefault(db, "DB_SSL_MODE")
		b.verifyDBPassword = "cgapp_" + randomSecret()
	}
	if cache := sol.ByKind[contract.KindCache]; cache != nil && !cache.Loose {
		b.cacheHost = producedDefault(cache, "REDIS_HOST")
		b.cachePort = strconv.Itoa(containerPort(cache, "redis"))
		b.cachePassword = producedDefault(cache, "REDIS_PASSWORD")
		b.cacheDB = producedDefault(cache, "REDIS_DB_NUMBER")
	}
	return b
}

// resolveValue resolves one consumed env variable of the given component.
func (b *bindings) resolveValue(component *solver.Component, variable contract.EnvVar) string {
	switch {
	case variable.From == "":
		return variable.Default
	case strings.HasPrefix(variable.From, "self.port."):
		name := strings.TrimPrefix(variable.From, "self.port.")
		return strconv.Itoa(containerPort(component, name))
	case strings.HasPrefix(variable.From, "database."):
		field := strings.TrimPrefix(variable.From, "database.")
		return b.databaseAttr(field)
	case strings.HasPrefix(variable.From, "cache."):
		field := strings.TrimPrefix(variable.From, "cache.")
		return b.cacheAttr(field)
	default:
		return variable.Default
	}
}

func (b *bindings) databaseAttr(field string) string {
	switch field {
	case "host":
		return b.dbHost
	case "port":
		return b.dbPort
	case "user":
		return b.dbUser
	case "password":
		return b.dbPassword
	case "name":
		return b.dbName
	case "sslmode":
		return b.dbSSLMode
	default:
		return ""
	}
}

func (b *bindings) cacheAttr(field string) string {
	switch field {
	case "host":
		return b.cacheHost
	case "port":
		return b.cachePort
	case "password":
		return b.cachePassword
	case "db":
		return b.cacheDB
	default:
		return ""
	}
}

// producedDefault returns the default value a component produces under name.
func producedDefault(component *solver.Component, name string) string {
	for _, variable := range component.Env.Produces {
		if variable.Name == name {
			return variable.Default
		}
	}
	return ""
}

// containerPort returns the in-container port declared for the named port.
func containerPort(component *solver.Component, name string) int {
	port, ok := component.FindPort(name)
	if !ok {
		return 0
	}
	return port.Port
}

func writeBackendEnv(workDir string, sol *solver.Solution, b *bindings) ([]string, error) {
	backend := sol.ByKind[contract.KindBackend]
	if backend == nil || backend.Loose || len(backend.Env.Consumes) == 0 {
		return nil, nil
	}
	fileName := backend.Glue.EnvFile
	if fileName == "" {
		fileName = ".env"
	}
	lines := []string{"# Generated by Create Go App CLI from the component capability contract."}
	for _, variable := range backend.Env.Consumes {
		value := b.resolveValue(backend, variable)
		if variable.Help != "" {
			lines = append(lines, "# "+variable.Help)
		}
		lines = append(lines, fmt.Sprintf("%s=%s", variable.Name, value))
	}
	path := filepath.Join(workDir, "backend", fileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		return nil, err
	}
	return []string{filepath.Join("backend", fileName)}, nil
}

func writeFrontendEnv(workDir string, sol *solver.Solution, b *bindings, domain string) ([]string, error) {
	frontend := sol.ByKind[contract.KindFrontend]
	if frontend == nil || frontend.Loose || frontend.Glue.APIURLEnv == "" {
		return nil, nil
	}
	fileName := frontend.Glue.EnvFile
	if fileName == "" {
		fileName = ".env.local"
	}
	apiURL := resolveAPIURL(sol, b, domain)
	content := strings.Join([]string{
		"# Generated by Create Go App CLI from the component capability contract.",
		fmt.Sprintf("%s=%s", frontend.Glue.APIURLEnv, apiURL),
		"",
	}, "\n")
	path := filepath.Join(workDir, "frontend", fileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return nil, err
	}
	return []string{filepath.Join("frontend", fileName)}, nil
}

// resolveAPIURL points the frontend at the proxy domain when an ingress
// exists, otherwise directly at the backend port.
func resolveAPIURL(sol *solver.Solution, b *bindings, domain string) string {
	base := sol.APIBasePath
	if proxy := sol.ByKind[contract.KindProxy]; proxy != nil && !proxy.Loose {
		return fmt.Sprintf("http://%s%s", domain, base)
	}
	return fmt.Sprintf("http://localhost:%d%s", b.backendHTTPPort, base)
}

func randomSecret() string {
	data := make([]byte, 8)
	if _, err := rand.Read(data); err != nil {
		return "changeme"
	}
	return hex.EncodeToString(data)
}
