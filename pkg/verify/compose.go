// Copyright 2023 Vic Shóstak and Create Go App Contributors. All rights reserved.
// Use of this source code is governed by Apache 2.0 license
// that can be found in the LICENSE file.

package verify

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// ComposeCLI is a located Docker Compose command (plugin or standalone).
type ComposeCLI struct {
	// Bin is the executable ("docker" or "docker-compose").
	Bin string
	// Prefix precedes Compose subcommands (e.g. ["compose"]).
	Prefix []string
}

// DetectCompose finds a working Docker Compose installation.
func DetectCompose(ctx context.Context) (*ComposeCLI, error) {
	if _, err := exec.LookPath("docker"); err == nil {
		cli := &ComposeCLI{Bin: "docker", Prefix: []string{"compose"}}
		if cli.supports(ctx) {
			return cli, nil
		}
	}
	if path, err := exec.LookPath("docker-compose"); err == nil {
		return &ComposeCLI{Bin: path}, nil
	}
	return nil, fmt.Errorf(
		"docker with the Compose plugin (or docker-compose) was not found in PATH; " +
			"install Docker Desktop/Docker Engine to run contract verification, " +
			"or rerun with --no-verify",
	)
}

func (c *ComposeCLI) supports(ctx context.Context) bool {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, c.Bin, append(append([]string{}, c.Prefix...), "version")...) // #nosec G204
	cmd.Stderr = &stderr
	return cmd.Run() == nil
}

// command builds a full Compose command for the plan.
func (c *ComposeCLI) command(ctx context.Context, plan *Plan, args ...string) *exec.Cmd {
	fullArgs := append([]string{}, c.Prefix...)
	fullArgs = append(fullArgs, "-f", plan.ComposeFile, "-p", plan.ProjectName)
	fullArgs = append(fullArgs, args...)
	cmd := exec.CommandContext(ctx, c.Bin, fullArgs...) // #nosec G204
	cmd.Dir = plan.WorkDir
	return cmd
}

// run executes a Compose command and returns its combined output.
func (c *ComposeCLI) run(ctx context.Context, plan *Plan, args ...string) (string, error) {
	var output bytes.Buffer
	cmd := c.command(ctx, plan, args...)
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	return output.String(), err
}

// Up starts the given services with image builds.
func (c *ComposeCLI) Up(ctx context.Context, plan *Plan, services []string) (string, error) {
	args := []string{"up", "-d", "--build"}
	return c.run(ctx, plan, append(args, services...)...)
}

// RunOnce runs a one-shot service (e.g. migrations) and returns its output.
func (c *ComposeCLI) RunOnce(ctx context.Context, plan *Plan, service string) (string, error) {
	return c.run(ctx, plan, "run", "--rm", "--build", service)
}

// Down removes the stack, its volumes and anonymous volumes.
func (c *ComposeCLI) Down(ctx context.Context, plan *Plan) (string, error) {
	return c.run(ctx, plan, "down", "-v", "--remove-orphans")
}

// Logs collects logs for all known services.
func (c *ComposeCLI) Logs(ctx context.Context, plan *Plan) string {
	if len(plan.AllServices) == 0 {
		return ""
	}
	output, err := c.run(ctx, plan, append([]string{"logs", "--no-color"}, plan.AllServices...)...)
	if err != nil && strings.TrimSpace(output) == "" {
		return fmt.Sprintf("failed to collect container logs: %v", err)
	}
	return output
}
