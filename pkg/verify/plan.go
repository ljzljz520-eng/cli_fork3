// Copyright 2023 Vic Shóstak and Create Go App Contributors. All rights reserved.
// Use of this source code is governed by Apache 2.0 license
// that can be found in the LICENSE file.

// Package verify runs the generated project in an ephemeral Docker Compose
// stack and executes contract probes against it.
package verify

import "time"

// ProbeKind enumerates contract probe types.
type ProbeKind string

const (
	// ProbeHTTP issues an HTTP request and checks the status code.
	ProbeHTTP ProbeKind = "http"
	// ProbeTCP dials a TCP address.
	ProbeTCP ProbeKind = "tcp"
	// ProbeCORS issues an HTTP request with Origin and checks CORS headers.
	ProbeCORS ProbeKind = "cors"
)

// Probe is one contract test executed against the ephemeral stack.
type Probe struct {
	Name         string
	Kind         ProbeKind
	URL          string
	Method       string
	Host         string
	Origin       string
	ExpectStatus int
	// ExpectHeader, when set, must be present and non-empty in the response.
	ExpectHeader string
}

// Plan is the fully resolved ephemeral verification plan.
type Plan struct {
	WorkDir        string
	ComposeFile    string
	ProjectName    string
	DataServices   []string // databases/caches started first
	AppServices    []string // backend/proxy started after migrations
	MigrateService string   // one-shot migration service, empty to skip
	AllServices    []string
	Probes         []Probe
	BuildTimeout   time.Duration
	WaitTimeout    time.Duration
}
