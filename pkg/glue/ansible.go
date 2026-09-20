// Copyright 2023 Vic Shóstak and Create Go App Contributors. All rights reserved.
// Use of this source code is governed by Apache 2.0 license
// that can be found in the LICENSE file.

package glue

import (
	"os"
	"path/filepath"
	"strconv"
	"text/template"

	"github.com/create-go-app/cli/v4/pkg/contract"
	"github.com/create-go-app/cli/v4/pkg/registry"
	"github.com/create-go-app/cli/v4/pkg/solver"
)

// InventoryData is the data model for hosts.ini.
type InventoryData struct {
	Marker        string
	ProjectDomain string
	ProxyName     string // none|traefik|nginx
	Wildcard      bool
	BackendPort   int
	DatabaseName  string // none|postgres
	Postgres      PostgresVars
	CacheName     string // none|redis
	Redis         RedisVars
	Traefik       TraefikVars
	Nginx         NginxVars
}

// PostgresVars parameterizes the postgres role.
type PostgresVars struct {
	ContainerName  string
	Version        string
	Port           int
	User           string
	Password       string
	DB             string
	SSLMode        string
	MigrateNumber  string
	MigrationsPath string
}

// RedisVars parameterizes the redis role.
type RedisVars struct {
	ContainerName string
	Version       string
	Port          int
}

// TraefikVars parameterizes the traefik role.
type TraefikVars struct {
	Version           string
	LogLevel          string
	LogFormat         string
	DashboardURL      string
	DashboardUser     string
	DashboardPassword string
	AcmeEmail         string
}

// NginxVars parameterizes the nginx role.
type NginxVars struct {
	Version          string
	UseOnlyHTTPS     string // yes|no
	RedirectToNonWWW string // yes|no
}

type playbookData struct {
	RoleProxy    string // none|traefik|nginx
	RoleDatabase string // none|postgres
	RoleCache    string // none|redis
}

// renderAnsible writes playbook.yml and hosts.ini from solution bindings.
func renderAnsible(workDir string, sol *solver.Solution, b *bindings, domain string) (string, InventoryData, error) {
	inventory := buildInventory(sol, b, domain)
	playbook := playbookData{
		RoleProxy:    inventory.ProxyName,
		RoleDatabase: inventory.DatabaseName,
		RoleCache:    inventory.CacheName,
	}

	tmpl, err := template.ParseFS(registry.EmbedTemplates, "templates/playbook.yml.tmpl", "templates/hosts.ini.tmpl")
	if err != nil {
		return "", inventory, err
	}

	playbookPath := filepath.Join(workDir, "playbook.yml")
	playbookFile, err := os.Create(playbookPath)
	if err != nil {
		return "", inventory, err
	}
	defer playbookFile.Close()
	if err := tmpl.ExecuteTemplate(playbookFile, "playbook.yml.tmpl", playbook); err != nil {
		return "", inventory, err
	}

	inventoryPath := filepath.Join(workDir, "hosts.ini")
	inventoryFile, err := os.Create(inventoryPath)
	if err != nil {
		return "", inventory, err
	}
	defer inventoryFile.Close()
	if err := tmpl.ExecuteTemplate(inventoryFile, "hosts.ini.tmpl", inventory); err != nil {
		return "", inventory, err
	}

	return "playbook.yml", inventory, nil
}

func buildInventory(sol *solver.Solution, b *bindings, domain string) InventoryData {
	inventory := InventoryData{
		Marker:        contract.SchemaVersion,
		ProjectDomain: domain,
		ProxyName:     "none",
		DatabaseName:  "none",
		CacheName:     "none",
		BackendPort:   b.backendHTTPPort,
		Traefik: TraefikVars{
			Version:           "latest",
			LogLevel:          "ERROR",
			LogFormat:         "json",
			DashboardURL:      "",
			DashboardUser:     "admin",
			DashboardPassword: "admin:$$apr1$$WpxRpfMZ$$TMTfGB37C9xAHiPIDiFiB1",
			AcmeEmail:         "mail@example.com",
		},
		Nginx: NginxVars{
			Version:          "alpine",
			UseOnlyHTTPS:     "yes",
			RedirectToNonWWW: "yes",
		},
	}

	if proxy := sol.ByKind[contract.KindProxy]; proxy != nil && !proxy.Loose {
		switch proxy.Name {
		case "traefik", "traefik-acme-dns":
			inventory.ProxyName = "traefik"
			inventory.Wildcard = proxy.Name == "traefik-acme-dns"
		case "nginx":
			inventory.ProxyName = "nginx"
		}
	}

	if db := sol.ByKind[contract.KindDatabase]; db != nil && !db.Loose {
		inventory.DatabaseName = "postgres"
		vars := PostgresVars{
			ContainerName: b.dbHost,
			Version:       orLatest(db.Runtime.Version),
			Port:          atoiOr(b.dbPort, 5432),
			User:          b.dbUser,
			Password:      b.dbPassword,
			DB:            b.dbName,
			SSLMode:       b.dbSSLMode,
		}
		if backend := sol.ByKind[contract.KindBackend]; backend != nil && !backend.Loose {
			vars.MigrationsPath = "backend/" + backend.Resources.MigrationsPath
			vars.MigrateNumber = backend.Resources.MigrationsNumber
		}
		inventory.Postgres = vars
	}

	if cache := sol.ByKind[contract.KindCache]; cache != nil && !cache.Loose {
		inventory.CacheName = "redis"
		inventory.Redis = RedisVars{
			ContainerName: b.cacheHost,
			Version:       orLatest(cache.Runtime.Version),
			Port:          atoiOr(b.cachePort, 6379),
		}
	}

	return inventory
}

func atoiOr(value string, fallback int) int {
	if port, err := strconv.Atoi(value); err == nil {
		return port
	}
	return fallback
}

func orLatest(version string) string {
	if version == "" {
		return "latest"
	}
	return version
}
