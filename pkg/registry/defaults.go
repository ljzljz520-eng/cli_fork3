// Copyright 2023 Vic Shóstak and Create Go App Contributors. All rights reserved.
// Use of this source code is governed by Apache 2.0 license
// that can be found in the LICENSE file.

package registry

import (
	"embed"

	"github.com/AlecAivazis/survey/v2"
)

// CLIVersion version of Create Go App CLI.
const CLIVersion string = "4.1.0"

// Variables struct for Ansible variables (inventory, hosts).
type Variables struct {
	List map[string]interface{}
}

// CreateAnswers struct for a survey's answers for `create` command.
type CreateAnswers struct {
	Backend       string
	Frontend      string
	Database      string
	Cache         string
	Proxy         string
	AgreeCreation bool `survey:"agree"`
}

// DatabaseOptions/CacheOptions/ProxyOptions are the role choices shared by
// the default and the custom-template surveys.
var (
	DatabaseOptions = []string{"none", "postgres"}
	CacheOptions    = []string{"none", "redis"}
	ProxyOptions    = []string{"none", "traefik", "traefik-acme-dns", "nginx"}
)

var (
	// EmbedMiscFiles misc files and configs.
	//go:embed misc/*
	EmbedMiscFiles embed.FS

	// EmbedRoles Ansible roles.
	//go:embed roles/*
	EmbedRoles embed.FS

	// EmbedTemplates template files.
	//go:embed templates/*
	EmbedTemplates embed.FS

	// CreateQuestions survey's questions for `create` command.
	CreateQuestions = []*survey.Question{
		{
			Name: "backend",
			Prompt: &survey.Select{
				Message: "Choose a backend framework:",
				Options: []string{
					"net/http",
					"fiber",
					"chi",
				},
				Default:  "fiber",
				PageSize: 3,
			},
			Validate: survey.Required,
		},
		{
			Name: "frontend",
			Prompt: &survey.Select{
				Message: "Choose a frontend framework/library:",
				Help:    "Option with a `*-ts` tail will create a TypeScript template.",
				Options: []string{
					"none",
					"vanilla",
					"vanilla-ts",
					"react",
					"react-ts",
					"react-swc",
					"react-swc-ts",
					"preact",
					"preact-ts",
					"next",
					"next-ts",
					"nuxt",
					"vue",
					"vue-ts",
					"sveltekit",
					"svelte",
					"svelte-ts",
					"solid",
					"solid-ts",
					"lit",
					"lit-ts",
					"qwik",
					"qwik-ts",
				},
				Default:  "none",
				PageSize: 21,
			},
		},
		{
			Name: "database",
			Prompt: &survey.Select{
				Message:  "Choose a database:",
				Help:     "Compatibility with the backend template is validated before generation.",
				Options:  DatabaseOptions,
				Default:  "none",
				PageSize: 2,
			},
		},
		{
			Name: "cache",
			Prompt: &survey.Select{
				Message:  "Choose a cache:",
				Help:     "Compatibility with the backend template is validated before generation.",
				Options:  CacheOptions,
				Default:  "none",
				PageSize: 2,
			},
		},
		{
			Name: "proxy",
			Prompt: &survey.Select{
				Message:  "Choose a web/proxy server:",
				Options:  ProxyOptions,
				Default:  "none",
				PageSize: 4,
			},
		},
		{
			Name: "agree",
			Prompt: &survey.Confirm{
				Message: "If everything is okay, can I create this project for you? ;)",
				Default: true,
			},
		},
	}

	// CustomCreateQuestions survey's questions for `create -c` command.
	CustomCreateQuestions = []*survey.Question{
		{
			Name: "backend",
			Prompt: &survey.Input{
				Message: "Enter URL to the custom backend repository:",
			},
			Validate: survey.Required,
		},
		{
			Name: "frontend",
			Prompt: &survey.Input{
				Message: "Enter URL to the custom frontend repository:",
				Default: "none",
			},
		},
		{
			Name: "database",
			Prompt: &survey.Select{
				Message:  "Choose a database:",
				Options:  DatabaseOptions,
				Default:  "none",
				PageSize: 2,
			},
		},
		{
			Name: "cache",
			Prompt: &survey.Select{
				Message:  "Choose a cache:",
				Options:  CacheOptions,
				Default:  "none",
				PageSize: 2,
			},
		},
		{
			Name: "proxy",
			Prompt: &survey.Select{
				Message:  "Choose a web/proxy server:",
				Options:  ProxyOptions,
				Default:  "none",
				PageSize: 4,
			},
		},
		{
			Name: "agree",
			Prompt: &survey.Confirm{
				Message: "If everything is okay, can I create this project for you? ;)",
				Default: true,
			},
		},
	}
)
