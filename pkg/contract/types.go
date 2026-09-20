// Copyright 2023 Vic Shóstak and Create Go App Contributors. All rights reserved.
// Use of this source code is governed by Apache 2.0 license
// that can be found in the LICENSE file.

// Package contract defines versioned capability contracts for project
// components (backend, frontend, database, cache, proxy, deploy) and loads
// them from the embedded catalog or from cloned project templates.
package contract

// SchemaVersion is the contract schema version implemented by this CLI.
const SchemaVersion = "cgapp.io/v1"

// DiscoveredContractFileName is the file name a custom template ships to
// publish its own capability contract.
const DiscoveredContractFileName = "cgapp.contract.yaml"

// Kind enumerates component kinds participating in a stack.
type Kind string

const (
	KindBackend  Kind = "backend"
	KindFrontend Kind = "frontend"
	KindDatabase Kind = "database"
	KindCache    Kind = "cache"
	KindProxy    Kind = "proxy"
	KindDeploy   Kind = "deploy"
)

// Port describes a single network port exposed by a component.
type Port struct {
	// Name is referenced by health checks and glue wiring (e.g. "http").
	Name string `yaml:"name"`
	// Port is the in-container port.
	Port int `yaml:"port"`
	// HostPort is the preferred port published to the host. Zero means the
	// port is not published by default (container network only).
	HostPort int `yaml:"hostPort"`
	// Protocol is "tcp" or "udp".
	Protocol string `yaml:"protocol"`
	// Candidates are fallback host ports tried by the solver when the
	// preferred HostPort is already in use.
	Candidates []int `yaml:"candidates"`
}

// RouteKind enumerates route purposes.
type RouteKind string

const (
	RouteAPI    RouteKind = "api"
	RouteHealth RouteKind = "health"
	RouteDocs   RouteKind = "docs"
)

// Route describes an HTTP route the component serves.
type Route struct {
	// Path is the absolute route path (e.g. "/api/v1", "/hc/status").
	Path string `yaml:"path"`
	// Kind is api|health|docs.
	Kind RouteKind `yaml:"kind"`
	// Method defaults to GET.
	Method string `yaml:"method"`
	// Expect is the HTTP status code contract probes must expect.
	Expect int `yaml:"expect"`
}

// CORSSpec describes Cross-Origin Resource Sharing capabilities.
type CORSSpec struct {
	// Enabled reports whether the component can answer cross-origin calls.
	Enabled bool `yaml:"enabled"`
	// DefaultAllow is the default allowed origin policy (e.g. "*").
	DefaultAllow string `yaml:"defaultAllow"`
}

// AuthSpec describes authentication and browser origin handling.
type AuthSpec struct {
	// Mechanism is none|jwt|basic|session.
	Mechanism string `yaml:"mechanism"`
	// CORS is the component's CORS capability.
	CORS CORSSpec `yaml:"cors"`
}

// RuntimeSpec describes the runtime a component needs.
type RuntimeSpec struct {
	// Language is go|node|image.
	Language string `yaml:"language"`
	// Version is a semver-ish constraint (e.g. ">=1.23", "20.x").
	Version string `yaml:"version"`
	// Image is the container image for image-based components (e.g. "postgres").
	Image string `yaml:"image"`
}

// EnvVar describes one environment variable a component consumes or produces.
type EnvVar struct {
	// Name is the environment variable name.
	Name string `yaml:"name"`
	// Required marks variables that must resolve to a value.
	Required bool `yaml:"required"`
	// Default is used when no producer supplies the value.
	Default string `yaml:"default"`
	// Secret marks values that must not leak into generated project files.
	Secret bool `yaml:"secret"`
	// From references a producer binding for glue resolution
	// (e.g. "self.port.http", "database.host", "api.url").
	From string `yaml:"from"`
	// Help is a human-readable comment for generated env files.
	Help string `yaml:"help"`
}

// EnvSpec groups consumed and produced variables.
type EnvSpec struct {
	Consumes []EnvVar `yaml:"consumes"`
	Produces []EnvVar `yaml:"produces"`
}

// HealthSpec is the contract for liveness/readiness probing.
type HealthSpec struct {
	// Type is http|tcp|exec.
	Type string `yaml:"type"`
	// PortName references a Port.Name for the probe target.
	PortName string `yaml:"portName"`
	// Path is the HTTP path for http probes (e.g. "/hc/status").
	Path string `yaml:"path"`
	// Expect is the expected HTTP status code.
	Expect int `yaml:"expect"`
	// Command is the in-container command for exec probes
	// (e.g. ["pg_isready", "-U", "postgres"]).
	Command []string `yaml:"command"`
	// Interval/Timeout are durations ("10s", "3s").
	Interval string `yaml:"interval"`
	Timeout  string `yaml:"timeout"`
	// Retries is the number of probe attempts before failure.
	Retries int `yaml:"retries"`
}

// Resources collects build/migration and resource declarations.
type Resources struct {
	// Dockerfile is the relative path to the component Dockerfile.
	Dockerfile string `yaml:"dockerfile"`
	// BuildContext is the relative build context directory.
	BuildContext string `yaml:"buildContext"`
	// MigrationsPath is the relative directory holding migration files.
	MigrationsPath string `yaml:"migrationsPath"`
	// MigrateImage is the image used to apply migrations.
	MigrateImage string `yaml:"migrateImage"`
	// MigrationsNumber is passed to the migration tool ("" means all).
	MigrationsNumber string `yaml:"migrationsNumber"`
	// CPULimit/MemoryLimit are container resource limits.
	CPULimit    string `yaml:"cpuLimit"`
	MemoryLimit string `yaml:"memoryLimit"`
}

// Capability is a named, versioned and attributed offer or requirement.
//
// Examples:
//
//	{name: database, attributes: {driver: postgres}}
//	{name: cache, attributes: {driver: redis}}
//	{name: http-api, attributes: {basePath: /api/v1}}
//	{name: http-upstream}
//	{name: browser-api}          // cross-origin or same-origin reachable API
//	{name: ingress, attributes: {scheme: http, tls: acme}}
//	{name: migrations, attributes: {format: golang-migrate}}
type Capability struct {
	Name       string            `yaml:"name"`
	Version    string            `yaml:"version"`
	Attributes map[string]string `yaml:"attributes"`
}

// Contract is the versioned capability contract of one component version.
type Contract struct {
	// APIVersion must equal SchemaVersion.
	APIVersion string `yaml:"apiVersion"`
	// Kind is one of the Kind* constants.
	Kind Kind `yaml:"kind"`
	// Name is the canonical component identifier (matches survey options).
	Name string `yaml:"name"`
	// Display is a human-readable component name.
	Display string `yaml:"display"`
	// Version is the component version this contract applies to.
	Version string `yaml:"version"`
	// Upstream notes the verified upstream revision for drift detection.
	Upstream string `yaml:"upstream"`

	Runtime   RuntimeSpec  `yaml:"runtime"`
	Ports     []Port       `yaml:"ports"`
	Routes    []Route      `yaml:"routes"`
	Auth      AuthSpec     `yaml:"auth"`
	Env       EnvSpec      `yaml:"env"`
	Health    *HealthSpec  `yaml:"health"`
	Resources Resources    `yaml:"resources"`
	Requires  []Capability `yaml:"requires"`
	Offers    []Capability `yaml:"offers"`

	// Glue holds component-family specific rendering hints.
	Glue GlueSpec `yaml:"glue"`
}

// GlueSpec carries code-generation hints consumed by the glue package.
type GlueSpec struct {
	// APIURLEnv is the frontend env var name receiving the API base URL.
	APIURLEnv string `yaml:"apiURLEnv"`
	// EnvFile is the relative env file name glue writes for this component.
	EnvFile string `yaml:"envFile"`
	// DevPort is the local dev-server port (informational for frontends).
	DevPort int `yaml:"devPort"`
}

// FindPort returns the port declaration with the given name, if any.
func (c *Contract) FindPort(name string) (Port, bool) {
	for _, p := range c.Ports {
		if p.Name == name {
			return p, true
		}
	}
	return Port{}, false
}

// FindRoute returns the first route of the given kind, if any.
func (c *Contract) FindRoute(kind RouteKind) (Route, bool) {
	for _, r := range c.Routes {
		if r.Kind == kind {
			return r, true
		}
	}
	return Route{}, false
}

// OffersCapability reports whether the contract offers the given capability
// name with all required attributes present (values must be equal).
func (c *Contract) OffersCapability(wanted Capability) bool {
	for _, offer := range c.Offers {
		if !MatchCapability(offer, wanted) {
			continue
		}
		if wanted.Version != "" && offer.Version != wanted.Version {
			continue
		}
		return true
	}
	return false
}

// MatchCapability checks capability name and attribute subset equality.
func MatchCapability(offer, wanted Capability) bool {
	if offer.Name != wanted.Name {
		return false
	}
	for key, value := range wanted.Attributes {
		if offer.Attributes[key] != value {
			return false
		}
	}
	return true
}
