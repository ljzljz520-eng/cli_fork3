// Copyright 2023 Vic Shóstak and Create Go App Contributors. All rights reserved.
// Use of this source code is governed by Apache 2.0 license
// that can be found in the LICENSE file.

package glue

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/create-go-app/cli/v4/pkg/contract"
	"github.com/create-go-app/cli/v4/pkg/solver"
	"github.com/create-go-app/cli/v4/pkg/verify"
)

// Service names used in the ephemeral Compose stack. They intentionally match
// the hosts expected by the generated backend env files.
const (
	dbService      = "cgapp-postgres"
	cacheService   = "cgapp-redis"
	backendService = "cgapp-backend"
	nginxService   = "cgapp-nginx"
	traefikService = "cgapp-traefik"
	migrateSvc     = "migrate"
)

// Ephemeral proxy config files (excluded from project promotion).
const (
	NginxVerifyConfFile   = "nginx.verify.conf"
	TraefikVerifyConfFile = "traefik.verify.yml"
)

type composeFile struct {
	Services map[string]composeService `yaml:"services"`
}

type composeBuild struct {
	Context    string `yaml:"context"`
	Dockerfile string `yaml:"dockerfile,omitempty"`
}

type serviceDep struct {
	Condition string `yaml:"condition"`
}

type composeHealthcheck struct {
	Test     []string `yaml:"test"`
	Interval string   `yaml:"interval,omitempty"`
	Timeout  string   `yaml:"timeout,omitempty"`
	Retries  int      `yaml:"retries,omitempty"`
}

type composeService struct {
	Image       string                `yaml:"image,omitempty"`
	Build       *composeBuild         `yaml:"build,omitempty"`
	Command     []string              `yaml:"command,omitempty"`
	Environment map[string]string     `yaml:"environment,omitempty"`
	Ports       []string              `yaml:"ports,omitempty"`
	Volumes     []string              `yaml:"volumes,omitempty"`
	DependsOn   map[string]serviceDep `yaml:"depends_on,omitempty"`
	Healthcheck *composeHealthcheck   `yaml:"healthcheck,omitempty"`
	Restart     string                `yaml:"restart,omitempty"`
	Labels      map[string]string     `yaml:"labels,omitempty"`
}

// buildVerifyStack renders the ephemeral Compose stack, proxy configs and the
// contract probe plan. A nil plan means verification cannot run (e.g. a loose
// custom backend without a contract).
func buildVerifyStack(sol *solver.Solution, opts Options, b *bindings) (*verify.Plan, []string, error) {
	backend := sol.ByKind[contract.KindBackend]
	if backend == nil || backend.Loose || backend.Resources.Dockerfile == "" {
		return nil, nil, nil
	}

	stack := composeFile{Services: map[string]composeService{}}
	var (
		dataServices, appServices, allServices []string
		probes                                 []verify.Probe
		extraFiles                             []string
		migrateService                         string
	)

	// Database.
	if db := sol.ByKind[contract.KindDatabase]; db != nil && !db.Loose {
		hostPort := db.HostPorts["postgres"]
		stack.Services[dbService] = composeService{
			Image: imageRef(db.Runtime),
			Environment: map[string]string{
				"POSTGRES_USER":     b.dbUser,
				"POSTGRES_PASSWORD": b.verifyDBPassword,
				"POSTGRES_DB":       b.dbName,
			},
			Ports: []string{fmt.Sprintf("%d:%d", hostPort, containerPort(db, "postgres"))},
			Healthcheck: &composeHealthcheck{
				Test:     []string{"CMD-SHELL", "pg_isready -U $$POSTGRES_USER"},
				Interval: healthOr(db.Health, "10s", "interval"),
				Timeout:  healthOr(db.Health, "3s", "timeout"),
				Retries:  healthRetries(db.Health),
			},
			Restart: "unless-stopped",
		}
		dataServices = append(dataServices, dbService)
		allServices = append(allServices, dbService)
		probes = append(probes, verify.Probe{
			Name: "postgres-tcp", Kind: verify.ProbeTCP,
			URL: fmt.Sprintf("127.0.0.1:%d", hostPort),
		})

		if backend.Resources.MigrationsPath != "" {
			dsn := fmt.Sprintf(
				"postgres://%s:%s@%s:%s/%s?sslmode=%s",
				url.QueryEscape(b.dbUser), url.QueryEscape(b.verifyDBPassword),
				b.dbHost, b.dbPort, url.QueryEscape(b.dbName), b.dbSSLMode,
			)
			command := []string{"-path", "/migrations", "-database", dsn, "up"}
			if backend.Resources.MigrationsNumber != "" {
				command = append(command, backend.Resources.MigrationsNumber)
			}
			stack.Services[migrateSvc] = composeService{
				Image:   backend.Resources.MigrateImage,
				Command: command,
				Volumes: []string{"./backend/" + backend.Resources.MigrationsPath + ":/migrations"},
				DependsOn: map[string]serviceDep{
					dbService: {Condition: "service_healthy"},
				},
				Restart: "no",
			}
			migrateService = migrateSvc
			allServices = append(allServices, migrateSvc)
		}
	}

	// Cache.
	if cache := sol.ByKind[contract.KindCache]; cache != nil && !cache.Loose {
		hostPort := cache.HostPorts["redis"]
		stack.Services[cacheService] = composeService{
			Image: imageRef(cache.Runtime),
			Ports: []string{fmt.Sprintf("%d:%d", hostPort, containerPort(cache, "redis"))},
			Healthcheck: &composeHealthcheck{
				Test:     []string{"CMD", "redis-cli", "ping"},
				Interval: healthOr(cache.Health, "10s", "interval"),
				Timeout:  healthOr(cache.Health, "3s", "timeout"),
				Retries:  healthRetries(cache.Health),
			},
			Restart: "unless-stopped",
		}
		dataServices = append(dataServices, cacheService)
		allServices = append(allServices, cacheService)
		probes = append(probes, verify.Probe{
			Name: "redis-tcp", Kind: verify.ProbeTCP,
			URL: fmt.Sprintf("127.0.0.1:%d", hostPort),
		})
	}

	// Backend.
	backendHostPort := backend.HostPorts["http"]
	backendSvc := composeService{
		Build: &composeBuild{
			Context:    "./backend",
			Dockerfile: backend.Resources.Dockerfile,
		},
		Environment: backendVerifyEnv(backend, b),
		Ports:       []string{fmt.Sprintf("%d:%d", backendHostPort, b.backendHTTPPort)},
		DependsOn:   map[string]serviceDep{},
		Restart:     "unless-stopped",
	}
	if sol.ByKind[contract.KindDatabase] != nil {
		backendSvc.DependsOn[dbService] = serviceDep{Condition: "service_healthy"}
	}
	if sol.ByKind[contract.KindCache] != nil {
		backendSvc.DependsOn[cacheService] = serviceDep{Condition: "service_healthy"}
	}
	stack.Services[backendService] = backendSvc
	appServices = append(appServices, backendService)
	allServices = append(allServices, backendService)

	if backend.Health != nil {
		switch backend.Health.Type {
		case "http":
			probes = append(probes, verify.Probe{
				Name:         "backend-health",
				Kind:         verify.ProbeHTTP,
				URL:          fmt.Sprintf("http://127.0.0.1:%d%s", backendHostPort, backend.Health.Path),
				ExpectStatus: expectStatus(backend.Health),
			})
		case "tcp":
			probes = append(probes, verify.Probe{
				Name: "backend-tcp", Kind: verify.ProbeTCP,
				URL: fmt.Sprintf("127.0.0.1:%d", backendHostPort),
			})
		}
	}

	// CORS probe: browser frontend, no same-origin proxy, CORS-capable backend.
	frontend := sol.ByKind[contract.KindFrontend]
	if frontend != nil && sol.ByKind[contract.KindProxy] == nil && backend.Auth.CORS.Enabled && backend.Health != nil && backend.Health.Type == "http" {
		probes = append(probes, verify.Probe{
			Name:         "backend-cors",
			Kind:         verify.ProbeCORS,
			URL:          fmt.Sprintf("http://127.0.0.1:%d%s", backendHostPort, backend.Health.Path),
			Origin:       fmt.Sprintf("http://localhost:%d", frontend.Glue.DevPort),
			ExpectStatus: expectStatus(backend.Health),
			ExpectHeader: "Access-Control-Allow-Origin",
		})
	}

	// Proxy.
	if proxy := sol.ByKind[contract.KindProxy]; proxy != nil && !proxy.Loose {
		proxyHostPort := proxy.HostPorts["http"]
		switch proxy.Name {
		case "nginx":
			confPath := filepath.Join(opts.WorkDir, NginxVerifyConfFile)
			content := fmt.Sprintf(nginxVerifyConf, opts.ProjectDomain, b.backendHTTPPort)
			if err := os.WriteFile(confPath, []byte(content), 0o600); err != nil {
				return nil, nil, err
			}
			extraFiles = append(extraFiles, NginxVerifyConfFile)
			stack.Services[nginxService] = composeService{
				Image: imageRef(proxy.Runtime),
				Ports: []string{fmt.Sprintf("%d:80", proxyHostPort)},
				Volumes: []string{
					"./" + NginxVerifyConfFile + ":/etc/nginx/conf.d/default.conf:ro",
				},
				DependsOn: map[string]serviceDep{
					backendService: {Condition: "service_started"},
				},
				Restart: "unless-stopped",
			}
			appServices = append(appServices, nginxService)
			allServices = append(allServices, nginxService)
			probes = appendProxyProbes(probes, "nginx", proxyHostPort, backend, opts.ProjectDomain)
		case "traefik", "traefik-acme-dns":
			confPath := filepath.Join(opts.WorkDir, TraefikVerifyConfFile)
			if err := os.WriteFile(confPath, []byte(traefikVerifyConf), 0o600); err != nil {
				return nil, nil, err
			}
			extraFiles = append(extraFiles, TraefikVerifyConfFile)
			backendSvc.Labels = map[string]string{
				"traefik.enable":                                         "true",
				"traefik.http.routers.backend.rule":                      fmt.Sprintf("Host(`%s`)", opts.ProjectDomain),
				"traefik.http.routers.backend.entrypoints":               "web",
				"traefik.http.services.backend.loadbalancer.server.port": strconv.Itoa(b.backendHTTPPort),
			}
			stack.Services[backendService] = backendSvc
			stack.Services[traefikService] = composeService{
				Image: imageRef(proxy.Runtime),
				Ports: []string{fmt.Sprintf("%d:80", proxyHostPort)},
				Volumes: []string{
					"/var/run/docker.sock:/var/run/docker.sock:ro",
					"./" + TraefikVerifyConfFile + ":/etc/traefik/traefik.yml:ro",
				},
				DependsOn: map[string]serviceDep{
					backendService: {Condition: "service_started"},
				},
				Restart: "unless-stopped",
			}
			appServices = append(appServices, traefikService)
			allServices = append(allServices, traefikService)
			probes = appendProxyProbes(probes, "traefik", proxyHostPort, backend, opts.ProjectDomain)
		}
	}

	data, err := yaml.Marshal(stack)
	if err != nil {
		return nil, nil, err
	}
	header := "# Auto-generated by Create Go App CLI for ephemeral contract verification.\n" +
		"# This file is temporary and is never added to the generated project.\n"
	composePath := filepath.Join(opts.WorkDir, ComposeFileName)
	if err := os.WriteFile(composePath, append([]byte(header), data...), 0o600); err != nil {
		return nil, nil, err
	}
	extraFiles = append(extraFiles, ComposeFileName)

	plan := &verify.Plan{
		WorkDir:        opts.WorkDir,
		ComposeFile:    ComposeFileName,
		ProjectName:    opts.ProjectName,
		DataServices:   dataServices,
		AppServices:    appServices,
		MigrateService: migrateService,
		AllServices:    allServices,
		Probes:         probes,
		BuildTimeout:   10 * time.Minute,
		WaitTimeout:    3 * time.Minute,
	}
	return plan, extraFiles, nil
}

// backendVerifyEnv builds the backend container environment for the verify
// stack; only variables the contract actually consumes are emitted.
func backendVerifyEnv(backend *solver.Component, b *bindings) map[string]string {
	consumed := map[string]bool{}
	for _, variable := range backend.Env.Consumes {
		consumed[variable.Name] = true
	}
	set := func(env map[string]string, name, value string) {
		if consumed[name] {
			env[name] = value
		}
	}
	env := map[string]string{}
	set(env, "SERVER_PORT", strconv.Itoa(b.backendHTTPPort))
	set(env, "DB_HOST", b.dbHost)
	set(env, "DB_PORT", b.dbPort)
	set(env, "DB_USER", b.dbUser)
	set(env, "DB_PASSWORD", b.verifyDBPassword)
	set(env, "DB_NAME", b.dbName)
	set(env, "DB_SSL_MODE", b.dbSSLMode)
	set(env, "REDIS_HOST", b.cacheHost)
	set(env, "REDIS_PORT", b.cachePort)
	// The ephemeral Redis runs without authentication.
	set(env, "REDIS_PASSWORD", "")
	set(env, "REDIS_DB_NUMBER", b.cacheDB)
	return env
}

func appendProxyProbes(probes []verify.Probe, proxyName string, proxyHostPort int, backend *solver.Component, domain string) []verify.Probe {
	probes = append(probes, verify.Probe{
		Name: proxyName + "-tcp", Kind: verify.ProbeTCP,
		URL: fmt.Sprintf("127.0.0.1:%d", proxyHostPort),
	})
	if backend.Health != nil && backend.Health.Type == "http" {
		probes = append(probes, verify.Probe{
			Name:         proxyName + "-route",
			Kind:         verify.ProbeHTTP,
			URL:          fmt.Sprintf("http://127.0.0.1:%d%s", proxyHostPort, backend.Health.Path),
			Host:         domain,
			ExpectStatus: expectStatus(backend.Health),
		})
	}
	return probes
}

// imageRef renders image:tag, defaulting to latest when no version is set.
func imageRef(runtime contract.RuntimeSpec) string {
	tag := runtime.Version
	if tag == "" {
		tag = "latest"
	}
	return fmt.Sprintf("%s:%s", runtime.Image, tag)
}

func expectStatus(health *contract.HealthSpec) int {
	if health.Expect != 0 {
		return health.Expect
	}
	return 200
}

func healthOr(health *contract.HealthSpec, fallback, field string) string {
	if health == nil {
		return fallback
	}
	if field == "interval" && health.Interval != "" {
		return health.Interval
	}
	if field == "timeout" && health.Timeout != "" {
		return health.Timeout
	}
	return fallback
}

func healthRetries(health *contract.HealthSpec) int {
	if health != nil && health.Retries > 0 {
		return health.Retries
	}
	return 12
}

const nginxVerifyConf = `# Ephemeral Nginx config for cgapp contract verification.
server {
    listen 80;
    server_name %s;

    location / {
        resolver 127.0.0.11 valid=10s;
        set $backend cgapp-backend:%d;
        proxy_pass http://$backend;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_redirect off;
    }
}
`

const traefikVerifyConf = `# Ephemeral Traefik config for cgapp contract verification.
# TLS/ACME is intentionally disabled: only HTTP routing is contract-tested.
entryPoints:
  web:
    address: :80

providers:
  docker:
    exposedByDefault: false
`
