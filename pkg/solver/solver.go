// Copyright 2023 Vic Shóstak and Create Go App Contributors. All rights reserved.
// Use of this source code is governed by Apache 2.0 license
// that can be found in the LICENSE file.

// Package solver selects mutually compatible component contracts with a
// small backtracking constraint solver: capability matching (requires ↔
// offers), semver version ranges, unique host port allocation and
// environment variable producer/consumer checks.
package solver

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/create-go-app/cli/v4/pkg/contract"
)

// Selection is the user input: selected component names ("none" disables an
// optional role) plus contracts discovered in custom templates.
type Selection struct {
	Backend        string
	Frontend       string
	Database       string
	Cache          string
	Proxy          string
	CustomBackend  *contract.Contract
	CustomFrontend *contract.Contract
}

// Options tunes solving.
type Options struct {
	// IsPortFree reports whether a host port can be bound. nil treats every
	// port as free.
	IsPortFree func(port int) bool
	// IgnoreConflicts downgrades soft conflicts (capability/env/port issues
	// for resolvable components) to warnings. Unknown components always
	// remain hard errors. The resulting stack is best-effort.
	IgnoreConflicts bool
}

// Component is a resolved contract plus solver-assigned runtime values.
type Component struct {
	contract.Contract
	Source    string
	Loose     bool
	HostPorts map[string]int
}

// Conflict is a structured incompatibility report.
type Conflict struct {
	Component  string
	Capability string
	Reason     string
	Hint       string
}

func (c Conflict) String() string {
	msg := fmt.Sprintf("[%s] %s", c.Component, c.Reason)
	if c.Capability != "" {
		msg = fmt.Sprintf("[%s] requires %q: %s", c.Component, c.Capability, c.Reason)
	}
	if c.Hint != "" {
		msg += " (" + c.Hint + ")"
	}
	return msg
}

// Solution is a satisfiable stack.
type Solution struct {
	Components  []*Component
	ByKind      map[contract.Kind]*Component
	APIBasePath string
	Warnings    []string
}

// Solve resolves the selection against the catalog and returns either a
// Solution or one or more conflicts.
func Solve(catalog *contract.Catalog, sel Selection, opts Options) (*Solution, []Conflict) {
	var conflicts []Conflict

	components, resolutionConflicts, looseWarnings := resolveComponents(catalog, sel)
	conflicts = append(conflicts, resolutionConflicts...)
	if len(conflicts) > 0 {
		return nil, conflicts
	}

	stack := &Solution{
		ByKind:   map[contract.Kind]*Component{},
		Warnings: looseWarnings,
	}
	for _, kind := range componentOrder {
		if component := components[kind]; component != nil {
			stack.ByKind[kind] = component
			stack.Components = append(stack.Components, component)
		}
	}

	// report records a conflict as hard or, in IgnoreConflicts mode, as a
	// best-effort warning.
	report := func(conflict Conflict) {
		if opts.IgnoreConflicts {
			stack.Warnings = append(stack.Warnings, "ignored conflict: "+conflict.String())
			return
		}
		conflicts = append(conflicts, conflict)
	}

	// Structural checks on every concrete contract.
	for _, component := range stack.Components {
		if component.Loose {
			continue
		}
		if needsHealth(component.Kind) && component.Health == nil {
			report(Conflict{
				Component: component.Name,
				Reason:    "component is deployed in the verify stack but declares no health check",
				Hint:      "add a health section to the contract",
			})
		}
	}

	// Capability matching.
	for _, component := range stack.Components {
		if component.Loose {
			continue
		}
		for _, required := range component.Requires {
			conflict := requireCapability(stack, component, required)
			if conflict != nil {
				report(*conflict)
			}
		}
		for _, envConflict := range checkEnv(component, stack.ByKind) {
			report(envConflict)
		}
	}

	// Allocate unique host ports.
	for _, portConflict := range allocatePorts(stack, opts) {
		report(portConflict)
	}

	// Resolve the API base path offered by the backend.
	if backend := stack.ByKind[contract.KindBackend]; backend != nil && !backend.Loose {
		for _, offer := range backend.Offers {
			if offer.Name == "http-api" {
				stack.APIBasePath = offer.Attributes["basePath"]
			}
		}
	}

	if len(conflicts) > 0 {
		return nil, conflicts
	}
	return stack, nil
}

var componentOrder = []contract.Kind{
	contract.KindBackend,
	contract.KindDatabase,
	contract.KindCache,
	contract.KindProxy,
	contract.KindFrontend,
}

// resolveComponents turns the selection into concrete components.
func resolveComponents(catalog *contract.Catalog, sel Selection) (map[contract.Kind]*Component, []Conflict, []string) {
	components := map[contract.Kind]*Component{}
	var conflicts []Conflict
	var warnings []string

	// Backend is mandatory.
	switch {
	case sel.CustomBackend != nil:
		components[contract.KindBackend] = &Component{Contract: *sel.CustomBackend, Source: "discovered"}
	default:
		if found, ok := catalog.ByName(contract.KindBackend, sel.Backend); ok {
			components[contract.KindBackend] = &Component{Contract: found, Source: "embedded"}
		} else if sel.Backend == "custom" {
			components[contract.KindBackend] = looseComponent(contract.KindBackend)
			warnings = append(warnings, "custom backend ships no cgapp.contract.yaml; compatibility checks skipped for it")
		} else {
			conflicts = append(conflicts, Conflict{
				Component: sel.Backend,
				Reason:    "unknown backend selected",
			})
		}
	}

	resolveOptional := func(kind contract.Kind, name string, custom *contract.Contract) {
		if name == "" || name == "none" {
			return
		}
		if custom != nil {
			components[kind] = &Component{Contract: *custom, Source: "discovered"}
			return
		}
		if found, ok := catalog.ByName(kind, name); ok {
			components[kind] = &Component{Contract: found, Source: "embedded"}
			return
		}
		if name == "custom" {
			components[kind] = looseComponent(kind)
			warnings = append(warnings, fmt.Sprintf("custom %s ships no cgapp.contract.yaml; compatibility checks skipped for it", kind))
			return
		}
		conflicts = append(conflicts, Conflict{
			Component: name,
			Reason:    fmt.Sprintf("unknown %s selected", kind),
		})
	}

	resolveOptional(contract.KindFrontend, sel.Frontend, sel.CustomFrontend)
	resolveOptional(contract.KindDatabase, sel.Database, nil)
	resolveOptional(contract.KindCache, sel.Cache, nil)
	resolveOptional(contract.KindProxy, sel.Proxy, nil)

	return components, conflicts, warnings
}

func looseComponent(kind contract.Kind) *Component {
	return &Component{
		Contract: contract.Contract{Kind: kind, Name: "custom"},
		Source:   "discovered",
		Loose:    true,
	}
}

func needsHealth(kind contract.Kind) bool {
	switch kind {
	case contract.KindBackend, contract.KindDatabase, contract.KindCache, contract.KindProxy:
		return true
	default:
		return false
	}
}

// capabilityProviders maps capability names to the kinds that may offer them.
var capabilityProviders = map[string][]contract.Kind{
	"database":      {contract.KindDatabase},
	"cache":         {contract.KindCache},
	"migrations":    {contract.KindBackend},
	"http-api":      {contract.KindBackend},
	"http-upstream": {contract.KindBackend},
	"browser-api":   {contract.KindBackend, contract.KindProxy},
	"ingress":       {contract.KindProxy},
	"deploy":        {contract.KindDeploy},
}

// requireCapability enforces one requires-entry against the stack.
func requireCapability(stack *Solution, requirer *Component, required contract.Capability) *Conflict {
	providerKinds := capabilityProviders[required.Name]
	if providerKinds == nil {
		return &Conflict{
			Component:  requirer.Name,
			Capability: required.Name,
			Reason:     "unknown capability name in contract",
		}
	}

	var concreteProviders, looseProviders int
	for _, kind := range providerKinds {
		provider := stack.ByKind[kind]
		if provider == nil || provider == requirer {
			continue
		}
		if provider.Loose {
			looseProviders++
			continue
		}
		concreteProviders++
		for _, offer := range provider.Offers {
			if !contract.MatchCapability(offer, required) {
				continue
			}
			if satisfiesVersion(offer.Version, required.Version) {
				return nil
			}
		}
	}

	// A loose component could still provide it: warn, do not block.
	if concreteProviders == 0 && looseProviders > 0 {
		stack.Warnings = append(stack.Warnings, fmt.Sprintf(
			"%s needs %q but the %s has no contract; assuming compatibility",
			requirer.Name, required.Name, strings.Join(kindsToNames(providerKinds, stack), "/"),
		))
		return nil
	}
	if concreteProviders == 0 && looseProviders == 0 {
		return &Conflict{
			Component:  requirer.Name,
			Capability: required.Name,
			Reason:     fmt.Sprintf("no selected component provides %s", describeCapability(required)),
			Hint:       hintFor(required.Name),
		}
	}
	return &Conflict{
		Component:  requirer.Name,
		Capability: required.Name,
		Reason: fmt.Sprintf(
			"selected %s is incompatible: %s not satisfied",
			strings.Join(kindsToNames(providerKinds, stack), "/"), describeCapability(required),
		),
		Hint: hintFor(required.Name),
	}
}

// checkEnv verifies that every required consumed variable can be resolved.
func checkEnv(component *Component, byKind map[contract.Kind]*Component) []Conflict {
	var conflicts []Conflict
	for _, envVar := range component.Env.Consumes {
		if !envVar.Required || envVar.Default != "" {
			continue
		}
		if strings.HasPrefix(envVar.From, "self.") {
			continue
		}
		if envVar.From == "" {
			// Must be produced by name somewhere.
			if findProducer(envVar.Name, component.Kind, byKind) != nil {
				continue
			}
			conflicts = append(conflicts, Conflict{
				Component: component.Name,
				Reason:    fmt.Sprintf("required env %q has no producer or default", envVar.Name),
			})
			continue
		}
		// Reference like "database.host".
		parts := strings.SplitN(envVar.From, ".", 2)
		kind, ok := referenceKind(parts[0])
		if !ok {
			conflicts = append(conflicts, Conflict{
				Component: component.Name,
				Reason:    fmt.Sprintf("env %q references unknown producer %q", envVar.Name, parts[0]),
			})
			continue
		}
		producer := byKind[kind]
		if producer == nil {
			conflicts = append(conflicts, Conflict{
				Component:  component.Name,
				Capability: string(kind),
				Reason:     fmt.Sprintf("env %q requires %s but that role is disabled", envVar.Name, kind),
				Hint:       fmt.Sprintf("choose a %s or change the backend", kind),
			})
			continue
		}
		if producer.Loose {
			continue
		}
		if !producesAttribute(producer, parts[1]) {
			conflicts = append(conflicts, Conflict{
				Component: component.Name,
				Reason: fmt.Sprintf(
					"env %q expects %s.%s but %s contract does not provide it",
					envVar.Name, kind, parts[1], producer.Name,
				),
			})
		}
	}
	return conflicts
}

// producesAttribute maps "host"/"port"/"user"/... to offers/attrs or env names.
func producesAttribute(producer *Component, attribute string) bool {
	attrToEnv := map[string]string{
		"host":     "HOST",
		"port":     "PORT",
		"user":     "USER",
		"password": "PASSWORD",
		"name":     "NAME",
		"sslmode":  "SSL_MODE",
		"db":       "DB_NUMBER",
	}
	envSuffix, ok := attrToEnv[attribute]
	if !ok {
		return false
	}
	for _, produced := range producer.Env.Produces {
		if strings.HasSuffix(produced.Name, envSuffix) || produced.Name == envSuffix {
			return true
		}
	}
	return false
}

func findProducer(envName string, consumerKind contract.Kind, byKind map[contract.Kind]*Component) *Component {
	for _, component := range byKind {
		if component.Kind == consumerKind || component.Loose {
			continue
		}
		for _, produced := range component.Env.Produces {
			if produced.Name == envName {
				return component
			}
		}
	}
	return nil
}

// allocatePorts assigns unique, free host ports to every published port.
func allocatePorts(stack *Solution, opts Options) []Conflict {
	used := map[int]bool{}
	var conflicts []Conflict
	for _, component := range stack.Components {
		if component.Loose {
			continue
		}
		component.HostPorts = map[string]int{}
		for _, port := range component.Ports {
			if port.HostPort == 0 {
				continue // container network only
			}
			candidates := append([]int{port.HostPort}, port.Candidates...)
			assigned := 0
			for _, candidate := range candidates {
				if used[candidate] {
					continue
				}
				if opts.IsPortFree != nil && !opts.IsPortFree(candidate) {
					continue
				}
				assigned = candidate
				break
			}
			if assigned == 0 {
				conflicts = append(conflicts, Conflict{
					Component: component.Name,
					Reason: fmt.Sprintf(
						"no free host port for %q (tried %v)", port.Name, candidates,
					),
				})
				continue
			}
			used[assigned] = true
			component.HostPorts[port.Name] = assigned
		}
	}
	return conflicts
}

// referenceKind maps an env From prefix to a component kind.
func referenceKind(prefix string) (contract.Kind, bool) {
	switch prefix {
	case "database":
		return contract.KindDatabase, true
	case "cache":
		return contract.KindCache, true
	case "api":
		return contract.KindBackend, true
	default:
		return "", false
	}
}

func kindsToNames(kinds []contract.Kind, stack *Solution) []string {
	names := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		if component := stack.ByKind[kind]; component != nil {
			names = append(names, string(kind)+" "+component.Name)
		} else {
			names = append(names, string(kind))
		}
	}
	sort.Strings(names)
	return names
}

func describeCapability(c contract.Capability) string {
	attrs := make([]string, 0, len(c.Attributes))
	keys := make([]string, 0, len(c.Attributes))
	for key := range c.Attributes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		attrs = append(attrs, key+"="+c.Attributes[key])
	}
	label := c.Name
	if len(attrs) > 0 {
		label += " (" + strings.Join(attrs, ", ") + ")"
	}
	return label
}

func hintFor(capability string) string {
	switch capability {
	case "database":
		return "choose postgres in the database question"
	case "cache":
		return "choose redis in the cache question, or a backend without cache requirements"
	case "migrations":
		return "the database role applies migrations; choose a backend that ships platform/migrations"
	case "http-api":
		return "choose a backend template that publishes API routes"
	case "browser-api":
		return "enable a proxy (nginx/traefik) or choose a backend with CORS support"
	case "http-upstream":
		return "choose a backend with an HTTP server"
	default:
		return ""
	}
}

// IsFreePort returns true when nothing is listening on the host port.
func IsFreePort(port int) bool {
	if port <= 0 || port > 65535 {
		return false
	}
	return probePort("tcp", "127.0.0.1:"+strconv.Itoa(port))
}
