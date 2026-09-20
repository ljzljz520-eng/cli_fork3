// Copyright 2023 Vic Shóstak and Create Go App Contributors. All rights reserved.
// Use of this source code is governed by Apache 2.0 license
// that can be found in the LICENSE file.

package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AlecAivazis/survey/v2"
	"github.com/spf13/cobra"

	"github.com/create-go-app/cli/v4/pkg/cgapp"
	"github.com/create-go-app/cli/v4/pkg/contract"
	"github.com/create-go-app/cli/v4/pkg/glue"
	"github.com/create-go-app/cli/v4/pkg/registry"
	"github.com/create-go-app/cli/v4/pkg/solver"
	"github.com/create-go-app/cli/v4/pkg/verify"
)

func init() {
	rootCmd.AddCommand(createCmd)
	createCmd.Flags().BoolVarP(
		&useCustomTemplate,
		"template", "t", false,
		"enables to use custom backend and frontend templates",
	)
	createCmd.Flags().BoolVarP(
		&noVerify,
		"no-verify", "", false,
		"skip the ephemeral Docker Compose contract verification",
	)
	createCmd.Flags().BoolVarP(
		&forceCompatibility,
		"force", "", false,
		"generate despite soft compatibility conflicts (verification still runs unless --no-verify)",
	)
}

// createCmd represents the `create` command.
var createCmd = &cobra.Command{
	Use:     "create",
	Aliases: []string{"new"},
	Short:   "Create a new project via interactive UI",
	Long:    "\nCreate a new project via interactive UI.",
	RunE:    runCreateCmd,
}

// runCreateCmd represents runner for the `create` command.
func runCreateCmd(cmd *cobra.Command, args []string) error {
	// Start message.
	cgapp.ShowMessage(
		"",
		fmt.Sprintf(
			"Create a new project via Create Go App CLI v%v...",
			registry.CLIVersion,
		),
		true, true,
	)

	// Start survey.
	if useCustomTemplate {
		if err := survey.Ask(
			registry.CustomCreateQuestions,
			&customCreateAnswers,
			survey.WithIcons(surveyIconsConfig),
		); err != nil {
			return cgapp.ShowError(err.Error())
		}
	} else {
		if err := survey.Ask(
			registry.CreateQuestions,
			&createAnswers,
			survey.WithIcons(surveyIconsConfig),
		); err != nil {
			return cgapp.ShowError(err.Error())
		}
	}

	// Catch the cancel action (hit "n" in the last question).
	if (!createAnswers.AgreeCreation && !useCustomTemplate) || (!customCreateAnswers.AgreeCreation && useCustomTemplate) {
		cgapp.ShowMessage(
			"",
			"Oh no! You said \"no\", so I won't create anything. Hope to see you soon!",
			true, true,
		)
		return nil
	}

	startTimer := time.Now()

	cwd, err := os.Getwd()
	if err != nil {
		return cgapp.ShowError(err.Error())
	}

	answers := selectedAnswers()
	if err := checkDestination(cwd, answers.frontend != "none"); err != nil {
		return cgapp.ShowError(err.Error())
	}

	// All generation happens inside a hidden staging directory; the cwd is
	// only touched after every check (including contract verification) passes.
	suffix := stageSuffix()
	stageDir := filepath.Join(cwd, ".cgapp-tmp-"+suffix)
	if err := os.MkdirAll(stageDir, 0o750); err != nil {
		return cgapp.ShowError(err.Error())
	}
	promoted := false
	defer func() {
		if !promoted {
			_ = os.RemoveAll(stageDir)
		}
	}()

	/*
		The project's backend part creation (phase 1: materialize sources).
	*/
	if err := cgapp.GitCloneTo(filepath.Join(stageDir, "backend"), answers.backendURL); err != nil {
		return cgapp.ShowError(err.Error())
	}
	cgapp.ShowMessage(
		"success",
		fmt.Sprintf("Backend was created with template `%v`!", answers.backendURL),
		true, false,
	)

	/*
		The project's frontend part creation.
	*/
	if answers.frontend != "none" {
		if useCustomTemplate {
			if err := cgapp.GitCloneTo(filepath.Join(stageDir, "frontend"), answers.frontend); err != nil {
				return cgapp.ShowError(err.Error())
			}
		} else {
			if err := scaffoldFrontend(stageDir, answers.frontend); err != nil {
				return cgapp.ShowError(err.Error())
			}
		}
		cgapp.RemoveFolders(filepath.Join(stageDir, "frontend"), []string{".git", ".github"})
		cgapp.ShowMessage(
			"success",
			fmt.Sprintf("Frontend was created with template `%v`!", answers.frontend),
			false, false,
		)
	}

	// Copy Ansible roles and misc files into the staging directory.
	if err := cgapp.CopyFromEmbeddedFSTo(
		stageDir,
		&cgapp.EmbeddedFileSystem{Name: registry.EmbedRoles, RootFolder: "roles", SkipDir: false},
	); err != nil {
		return cgapp.ShowError(err.Error())
	}
	if err := cgapp.CopyFromEmbeddedFSTo(
		stageDir,
		&cgapp.EmbeddedFileSystem{Name: registry.EmbedMiscFiles, RootFolder: "misc", SkipDir: true},
	); err != nil {
		return cgapp.ShowError(err.Error())
	}

	/*
		Phase 2: discover contracts, solve compatibility, render glue.
	*/
	catalog, err := contract.LoadEmbeddedCatalog()
	if err != nil {
		return cgapp.ShowError(err.Error())
	}

	selection := solver.Selection{
		Database: answers.database,
		Cache:    answers.cache,
		Proxy:    answers.proxy,
	}
	var discovered []contract.Contract

	if useCustomTemplate {
		selection.Backend = "custom"
		if backendContract, docs, err := discoverContract(filepath.Join(stageDir, "backend"), contract.KindBackend); err != nil {
			return cgapp.ShowError(err.Error())
		} else if backendContract != nil {
			selection.Backend = backendContract.Name
			selection.CustomBackend = backendContract
			discovered = append(discovered, docs...)
		} else {
			cgapp.ShowMessage("info",
				"Custom backend ships no cgapp.contract.yaml; compatibility checks and stack verification are skipped for it.",
				false, false)
		}
		if answers.frontend != "none" {
			selection.Frontend = "custom"
			if frontendContract, docs, err := discoverContract(filepath.Join(stageDir, "frontend"), contract.KindFrontend); err != nil {
				return cgapp.ShowError(err.Error())
			} else if frontendContract != nil {
				selection.Frontend = frontendContract.Name
				selection.CustomFrontend = frontendContract
				discovered = append(discovered, docs...)
			}
		}
	} else {
		selection.Backend = answers.backend
		selection.Frontend = answers.frontend
		// Default templates may ship a contract file in a future release;
		// discovered documents overlay the embedded catalog.
		if backendContract, docs, err := discoverContract(filepath.Join(stageDir, "backend"), contract.KindBackend); err != nil {
			return cgapp.ShowError(err.Error())
		} else if backendContract != nil {
			discovered = append(discovered, docs...)
		}
	}

	mergedCatalog, err := catalog.Merge(discovered)
	if err != nil {
		return cgapp.ShowError(err.Error())
	}

	solution, conflicts := solver.Solve(mergedCatalog, selection, solver.Options{
		IsPortFree:      solver.IsFreePort,
		IgnoreConflicts: forceCompatibility,
	})
	if len(conflicts) > 0 {
		for _, conflict := range conflicts {
			cgapp.ShowMessage("error", conflict.String(), false, false)
		}
		return cgapp.ShowError(
			"selected components are incompatible; nothing was created in your working directory",
		)
	}
	for _, warning := range solution.Warnings {
		cgapp.ShowMessage("info", "WARNING: "+warning, false, false)
	}

	result, err := glue.Generate(solution, glue.Options{
		WorkDir:       stageDir,
		ProjectName:   "cgapp_verify_" + suffix,
		ProjectDomain: "example.com",
	})
	if err != nil {
		return cgapp.ShowError(err.Error())
	}
	glue.PruneRoles(stageDir, answers.database, answers.cache, answers.proxy)
	cgapp.ShowMessage(
		"success",
		"Ansible inventory, playbook and roles were generated from capability contracts!",
		false, false,
	)

	/*
		Phase 3: run the full stack in an ephemeral Compose environment and
		execute contract probes (health, routes, CORS, migrations, ports).
	*/
	switch {
	case noVerify:
		cgapp.ShowMessage("info", "Contract verification skipped (--no-verify).", false, false)
	case result.Plan == nil:
		cgapp.ShowMessage("info",
			"Stack verification skipped: the selected backend publishes no capability contract.",
			false, false)
	default:
		cgapp.ShowMessage("info",
			"Starting ephemeral Docker Compose stack for contract verification...",
			false, false)
		ctx, cancel := context.WithTimeout(context.Background(), result.Plan.BuildTimeout+result.Plan.WaitTimeout+2*time.Minute)
		probeResults, logs, err := verify.Run(ctx, result.Plan)
		cancel()
		if err != nil {
			cgapp.ShowMessage("error", err.Error(), false, true)
			for _, probe := range probeResults {
				if !probe.OK {
					cgapp.ShowMessage("error",
						fmt.Sprintf("probe %q failed: %s", probe.Probe.Name, probe.Detail),
						false, false)
				}
			}
			if strings.TrimSpace(logs) != "" {
				cgapp.ShowMessage("", "Container logs:\n"+logs, false, false)
			}
			return cgapp.ShowError(
				"contract verification failed; review the errors above (nothing was added to your working directory)",
			)
		}
		cgapp.ShowMessage("success",
			fmt.Sprintf("Contract verification passed (%d probes)!", len(probeResults)),
			false, false)
	}

	/*
		Phase 4: promote staged files into the working directory.
	*/
	if err := promote(stageDir, cwd); err != nil {
		return cgapp.ShowError(err.Error())
	}
	promoted = true
	_ = os.Remove(stageDir)

	stopTimer := cgapp.CalculateDurationTime(startTimer)
	cgapp.ShowMessage("info", fmt.Sprintf("Completed in %v seconds!", stopTimer), true, true)

	cgapp.ShowMessage(
		"",
		"* Please put credentials into the Ansible inventory file (`hosts.ini`) before you start deploying a project!",
		false, false,
	)
	if !useCustomTemplate && answers.frontend != "none" {
		cgapp.ShowMessage(
			"",
			fmt.Sprintf("* Visit https://vitejs.dev/guide/ for more info about using the `%v` frontend template!", answers.frontend),
			false, false,
		)
	}
	cgapp.ShowMessage(
		"",
		"* A helpful documentation and next steps with your project is here https://github.com/create-go-app/cli/wiki",
		false, true,
	)
	cgapp.ShowMessage("", "Have a happy new project! :)", false, true)

	return nil
}

// createSelectionAnswers is the normalized survey result used by generation.
type createSelectionAnswers struct {
	backend    string
	backendURL string
	frontend   string
	database   string
	cache      string
	proxy      string
}

func selectedAnswers() createSelectionAnswers {
	if useCustomTemplate {
		return createSelectionAnswers{
			backend:    "custom",
			backendURL: customCreateAnswers.Backend,
			frontend:   customCreateAnswers.Frontend,
			database:   customCreateAnswers.Database,
			cache:      customCreateAnswers.Cache,
			proxy:      customCreateAnswers.Proxy,
		}
	}
	return createSelectionAnswers{
		backend: createAnswers.Backend,
		backendURL: fmt.Sprintf(
			"github.com/create-go-app/%v-go-template",
			strings.ReplaceAll(createAnswers.Backend, "/", "_"),
		),
		frontend: createAnswers.Frontend,
		database: createAnswers.Database,
		cache:    createAnswers.Cache,
		proxy:    createAnswers.Proxy,
	}
}

// scaffoldFrontend runs the JavaScript scaffolder inside the staging dir.
func scaffoldFrontend(stageDir, frontend string) error {
	switch frontend {
	case "next":
		return cgapp.ExecCommandInDir(
			stageDir, "npx",
			[]string{
				"create-next-app@latest", "frontend",
				"--javascript",
				"--eslint",
				"--app",
				"--tailwind", "false",
				"--src-dir", "false",
				"--import-alias", "false",
			}, true,
		)
	case "next-ts":
		return cgapp.ExecCommandInDir(
			stageDir, "npx",
			[]string{
				"create-next-app@latest", "frontend",
				"--typescript",
				"--eslint",
				"--app",
				"--tailwind", "false",
				"--src-dir", "false",
				"--import-alias", "false",
			}, true,
		)
	case "nuxt":
		return cgapp.ExecCommandInDir(
			stageDir, "npx",
			[]string{"nuxi@latest", "init", "frontend"}, true,
		)
	case "sveltekit":
		return cgapp.ExecCommandInDir(
			stageDir, "npm",
			[]string{
				"create", "@svelte-add/kit@latest", "frontend",
				"--",
				"--with", "typescript+eslint+prettier",
			}, true,
		)
	default:
		return cgapp.ExecCommandInDir(
			stageDir, "npm",
			[]string{"create", "vite@latest", "frontend", "--", "--template", frontend},
			true,
		)
	}
}

// discoverContract reads cgapp.contract.yaml from a materialized template.
func discoverContract(dir string, want contract.Kind) (*contract.Contract, []contract.Contract, error) {
	docs, err := contract.LoadFromDir(dir)
	if err != nil {
		return nil, nil, err
	}
	var picked *contract.Contract
	for i := range docs {
		if docs[i].Kind == want {
			picked = &docs[i]
		}
	}
	return picked, docs, nil
}

// checkDestination aborts before staging when any known output already exists.
func checkDestination(cwd string, withFrontend bool) error {
	destinations := []string{
		"backend", "roles", "playbook.yml", "hosts.ini",
		"Makefile", ".gitignore", ".gitattributes", ".editorconfig",
	}
	if withFrontend {
		destinations = append(destinations, "frontend")
	}
	for _, destination := range destinations {
		if _, err := os.Stat(filepath.Join(cwd, destination)); err == nil {
			return fmt.Errorf(
				"destination %q already exists in the current directory; run the CLI in an empty project folder",
				destination,
			)
		}
	}
	return nil
}

// promote moves every staged entry (except ephemeral verification files) into
// the target directory. All collisions are detected before any move.
func promote(stageDir, targetDir string) error {
	entries, err := os.ReadDir(stageDir)
	if err != nil {
		return err
	}
	ephemeral := map[string]bool{
		glue.ComposeFileName:       true,
		glue.NginxVerifyConfFile:   true,
		glue.TraefikVerifyConfFile: true,
	}
	var names []string
	for _, entry := range entries {
		if ephemeral[entry.Name()] {
			continue
		}
		names = append(names, entry.Name())
		if _, err := os.Stat(filepath.Join(targetDir, entry.Name())); err == nil {
			return fmt.Errorf(
				"destination %q already exists; aborting so nothing is overwritten",
				entry.Name(),
			)
		}
	}
	for _, name := range names {
		if err := os.Rename(filepath.Join(stageDir, name), filepath.Join(targetDir, name)); err != nil {
			return err
		}
	}
	return nil
}

func stageSuffix() string {
	return fmt.Sprintf("%x", time.Now().UnixNano())
}
