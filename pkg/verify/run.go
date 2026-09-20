// Copyright 2023 Vic Shóstak and Create Go App Contributors. All rights reserved.
// Use of this source code is governed by Apache 2.0 license
// that can be found in the LICENSE file.

package verify

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// ProbeResult is the outcome of one probe execution.
type ProbeResult struct {
	Probe  Probe
	OK     bool
	Detail string
}

// Run brings the ephemeral stack up, applies migrations, waits for health and
// executes every contract probe. The stack is always torn down (with volumes)
// before returning. Logs are returned only on failure for diagnostics.
func Run(ctx context.Context, plan *Plan) ([]ProbeResult, string, error) {
	if plan == nil {
		return nil, "", fmt.Errorf("no verification plan")
	}

	cli, err := DetectCompose(ctx)
	if err != nil {
		return nil, "", err
	}

	// Start from a clean slate: a previous interrupted run may have left
	// containers behind with credentials that differ from this run's.
	cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	_, _ = cli.Down(cleanupCtx, plan)
	cleanupCancel()

	// Teardown must work even when the caller cancels the context.
	defer func() {
		downContext, downCancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		if _, err := cli.Down(downContext, plan); err != nil {
			// Docker can briefly refuse while containers are stopping; retry once.
			downCancel()
			retryCtx, retryCancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
			time.Sleep(3 * time.Second)
			_, _ = cli.Down(retryCtx, plan)
			retryCancel()
			return
		}
		downCancel()
	}()

	fail := func(format string, args ...any) ([]ProbeResult, string, error) {
		message := fmt.Sprintf(format, args...)
		logsCtx, cancelLogs := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		logs := cli.Logs(logsCtx, plan)
		cancelLogs()
		return nil, logs, fmt.Errorf("contract verification failed: %s", message)
	}

	// 1. Data services (database/cache).
	if len(plan.DataServices) > 0 {
		buildCtx, cancel := context.WithTimeout(ctx, timeoutOr(plan.BuildTimeout, 10*time.Minute))
		output, err := cli.Up(buildCtx, plan, plan.DataServices)
		cancel()
		if err != nil {
			return fail("starting data services: %v\n%s", err, output)
		}
	}

	// 2. One-shot migrations must exit 0.
	if plan.MigrateService != "" {
		migrateCtx, cancel := context.WithTimeout(ctx, timeoutOr(plan.BuildTimeout, 10*time.Minute))
		output, err := cli.RunOnce(migrateCtx, plan, plan.MigrateService)
		cancel()
		if err != nil {
			return fail("database migration exited with error: %v\n%s", err, output)
		}
	}

	// 3. Application services (backend/proxy).
	if len(plan.AppServices) > 0 {
		buildCtx, cancel := context.WithTimeout(ctx, timeoutOr(plan.BuildTimeout, 10*time.Minute))
		output, err := cli.Up(buildCtx, plan, plan.AppServices)
		cancel()
		if err != nil {
			return fail("starting application services: %v\n%s", err, output)
		}
	}

	// 4. Wait until every probe passes.
	waitDeadline := time.Now().Add(timeoutOr(plan.WaitTimeout, 3*time.Minute))
	var results []ProbeResult
	for {
		results = runProbes(plan.Probes)
		if allPassed(results) {
			return results, "", nil
		}
		if time.Now().After(waitDeadline) {
			return fail("probes did not pass before deadline:\n%s", summarizeProbes(results))
		}
		select {
		case <-ctx.Done():
			return fail("verification interrupted: %v\n%s", ctx.Err(), summarizeProbes(results))
		case <-time.After(2 * time.Second):
		}
	}
}

func runProbes(probes []Probe) []ProbeResult {
	results := make([]ProbeResult, 0, len(probes))
	for _, probe := range probes {
		result := ProbeResult{Probe: probe}
		switch probe.Kind {
		case ProbeTCP:
			result.OK, result.Detail = probeTCP(probe)
		case ProbeHTTP:
			result.OK, result.Detail = probeHTTP(probe)
		case ProbeCORS:
			result.OK, result.Detail = probeCORS(probe)
		default:
			result.Detail = "unknown probe kind"
		}
		results = append(results, result)
	}
	return results
}

func allPassed(results []ProbeResult) bool {
	for _, result := range results {
		if !result.OK {
			return false
		}
	}
	return true
}

func summarizeProbes(results []ProbeResult) string {
	var lines []string
	for _, result := range results {
		status := "ok"
		if !result.OK {
			status = "FAIL"
		}
		line := fmt.Sprintf("  - [%s] %s (%s)", status, result.Probe.Name, result.Probe.URL)
		if !result.OK && result.Detail != "" {
			line += ": " + result.Detail
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func probeTCP(probe Probe) (bool, string) {
	conn, err := net.DialTimeout("tcp", probe.URL, 3*time.Second)
	if err != nil {
		return false, err.Error()
	}
	_ = conn.Close()
	return true, ""
}

func probeHTTP(probe Probe) (bool, string) {
	method := probe.Method
	if method == "" {
		method = http.MethodGet
	}
	req, err := http.NewRequest(method, probe.URL, nil)
	if err != nil {
		return false, err.Error()
	}
	if probe.Host != "" {
		req.Host = probe.Host
	}
	if probe.Origin != "" {
		req.Header.Set("Origin", probe.Origin)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false, err.Error()
	}
	defer resp.Body.Close()

	if resp.StatusCode != expectedStatus(probe) {
		return false, fmt.Sprintf("got status %d, want %d", resp.StatusCode, expectedStatus(probe))
	}
	if probe.ExpectHeader != "" && resp.Header.Get(probe.ExpectHeader) == "" {
		return false, fmt.Sprintf("missing response header %q", probe.ExpectHeader)
	}
	return true, ""
}

func probeCORS(probe Probe) (bool, string) {
	if probe.Origin == "" {
		return false, "CORS probe requires an Origin header value"
	}
	// probeHTTP applies Origin, status and ExpectHeader checks.
	probe.Kind = ProbeHTTP
	return probeHTTP(probe)
}

func expectedStatus(probe Probe) int {
	if probe.ExpectStatus != 0 {
		return probe.ExpectStatus
	}
	return http.StatusOK
}

func timeoutOr(value, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}
