// Copyright 2023 Vic Shóstak and Create Go App Contributors. All rights reserved.
// Use of this source code is governed by Apache 2.0 license
// that can be found in the LICENSE file.

package contract

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadEmbeddedCatalog(t *testing.T) {
	catalog, err := LoadEmbeddedCatalog()
	if err != nil {
		t.Fatalf("LoadEmbeddedCatalog() error = %v", err)
	}

	expected := map[Kind][]string{
		KindBackend:  {"fiber", "chi", "net/http"},
		KindFrontend: {"vanilla", "next-ts", "nuxt", "sveltekit", "qwik-ts"},
		KindDatabase: {"postgres"},
		KindCache:    {"redis"},
		KindProxy:    {"nginx", "traefik", "traefik-acme-dns"},
		KindDeploy:   {"ansible"},
	}
	for kind, names := range expected {
		for _, name := range names {
			if _, ok := catalog.ByName(kind, name); !ok {
				t.Errorf("catalog missing %s/%s", kind, name)
			}
		}
	}

	// All 22 frontend survey options must be covered.
	if frontends := len(catalog.All()); frontends < 22 {
		t.Errorf("catalog too small, got %d contracts", frontends)
	}
	var frontendCount int
	for _, item := range catalog.All() {
		if item.Kind == KindFrontend {
			frontendCount++
		}
	}
	if frontendCount != 22 {
		t.Errorf("frontend contracts = %d, want 22", frontendCount)
	}
}

func TestParseDocuments(t *testing.T) {
	data := []byte(`---
apiVersion: cgapp.io/v1
kind: cache
name: redis-a
version: "7"
---
apiVersion: cgapp.io/v1
kind: cache
name: redis-b
version: "6"
`)
	contracts, err := ParseDocuments(data)
	if err != nil {
		t.Fatalf("ParseDocuments() error = %v", err)
	}
	if len(contracts) != 2 {
		t.Fatalf("ParseDocuments() len = %d, want 2", len(contracts))
	}
	if contracts[0].Name != "redis-a" || contracts[1].Name != "redis-b" {
		t.Errorf("unexpected contract names: %q, %q", contracts[0].Name, contracts[1].Name)
	}
}

func TestValidate(t *testing.T) {
	valid := Contract{
		APIVersion: SchemaVersion,
		Kind:       KindBackend,
		Name:       "x",
		Ports:      []Port{{Name: "http", Port: 5000}},
		Routes:     []Route{{Path: "/hc"}},
	}
	if err := Validate(valid); err != nil {
		t.Errorf("Validate(valid) error = %v", err)
	}

	for name, mutate := range map[string]func(*Contract){
		"bad apiVersion": func(c *Contract) { c.APIVersion = "wrong/v0" },
		"bad kind":       func(c *Contract) { c.Kind = "unknown" },
		"empty name":     func(c *Contract) { c.Name = "" },
		"unnamed port":   func(c *Contract) { c.Ports[0].Name = "" },
		"zero port":      func(c *Contract) { c.Ports[0].Port = 0 },
		"bad route":      func(c *Contract) { c.Routes[0].Path = "hc" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := valid
			bad.Ports = append([]Port(nil), valid.Ports...)
			bad.Routes = append([]Route(nil), valid.Routes...)
			mutate(&bad)
			if err := Validate(bad); err == nil {
				t.Errorf("Validate() expected error for %s", name)
			}
		})
	}
}

func TestLoadFromDir(t *testing.T) {
	dir := t.TempDir()

	// Missing file is not an error.
	contracts, err := LoadFromDir(dir)
	if err != nil {
		t.Fatalf("LoadFromDir(missing) error = %v", err)
	}
	if contracts != nil {
		t.Fatalf("LoadFromDir(missing) = %v, want nil", contracts)
	}

	data := []byte(`apiVersion: cgapp.io/v1
kind: backend
name: my-custom
version: "1.0.0"
ports:
  - {name: http, port: 8080}
routes:
  - {path: /healthz, kind: health, expect: 200}
`)
	path := filepath.Join(dir, DiscoveredContractFileName)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	contracts, err = LoadFromDir(dir)
	if err != nil {
		t.Fatalf("LoadFromDir() error = %v", err)
	}
	if len(contracts) != 1 || contracts[0].Name != "my-custom" {
		t.Fatalf("unexpected contracts: %+v", contracts)
	}
	port, ok := contracts[0].FindPort("http")
	if !ok || port.Port != 8080 {
		t.Errorf("port not parsed: %+v (%v)", port, ok)
	}
}

func TestCatalogMerge(t *testing.T) {
	base, err := LoadEmbeddedCatalog()
	if err != nil {
		t.Fatal(err)
	}

	override := []Contract{{
		APIVersion: SchemaVersion,
		Kind:       KindBackend,
		Name:       "fiber",
		Version:    "99.0",
		Ports:      []Port{{Name: "http", Port: 5000}},
	}}
	merged, err := base.Merge(override)
	if err != nil {
		t.Fatalf("Merge() error = %v", err)
	}
	got, ok := merged.ByName(KindBackend, "fiber")
	if !ok {
		t.Fatal("fiber missing after merge")
	}
	if got.Version != "99.0" {
		t.Errorf("override not applied, version = %q", got.Version)
	}
	// Base catalog stays untouched.
	original, _ := base.ByName(KindBackend, "fiber")
	if original.Version == "99.0" {
		t.Error("Merge mutated the base catalog")
	}
}
