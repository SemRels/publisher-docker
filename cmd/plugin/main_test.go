// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The publisher-docker Authors

package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	plugin "github.com/SemRels/publisher-docker/internal/plugin"
	"github.com/stretchr/testify/require"
)

const (
	entrypointImageID = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	entrypointDigest  = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

type entrypointResponse struct {
	output plugin.CommandOutput
	err    error
}

type entrypointRunner struct {
	responses []entrypointResponse
	commands  []plugin.Command
}

func (r *entrypointRunner) Run(_ context.Context, command plugin.Command) (plugin.CommandOutput, error) {
	r.commands = append(r.commands, command)
	if len(r.responses) == 0 {
		return plugin.CommandOutput{}, errors.New("unexpected command")
	}
	response := r.responses[0]
	r.responses = r.responses[1:]
	return response.output, response.err
}

func TestRunPublishesAndReportsImmutableDigest(t *testing.T) {
	t.Parallel()

	runner := &entrypointRunner{responses: []entrypointResponse{
		{output: entrypointInspect(entrypointImageID)},
		{output: entrypointInspect(entrypointImageID)},
		{output: entrypointInspect(entrypointImageID)},
		{output: plugin.CommandOutput{Stdout: []byte("digest: " + entrypointDigest + " size: 123\n")}},
	}}
	env := map[string]string{
		"SEMREL_PLUGIN_IMAGE": "demo:build",
		"SEMREL_PLUGIN_REF":   "localhost:5000/team/demo:{version}",
		"SEMREL_VERSION":      "v1.2.3",
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run(context.Background(), &stdout, &stderr, entrypointEnv(env), runner)

	require.Equal(t, 0, code)
	require.Equal(t, "plugin_schema_version=1\n", stderr.String())
	require.Equal(
		t,
		"publisher-docker: published localhost:5000/team/demo:1.2.3 as localhost:5000/team/demo@"+entrypointDigest+"\n",
		stdout.String(),
	)
	require.Len(t, runner.commands, 4)
	require.Equal(t, []string{"image", "push", "localhost:5000/team/demo:1.2.3"}, runner.commands[3].Args)
}

func TestRunDryRunInspectsWithoutMutation(t *testing.T) {
	t.Parallel()

	runner := &entrypointRunner{responses: []entrypointResponse{
		{output: entrypointInspect(entrypointImageID)},
		{
			output: plugin.CommandOutput{Stderr: []byte("Error: No such image")},
			err:    errors.New("exit status 1"),
		},
	}}
	env := map[string]string{
		"SEMREL_PLUGIN_IMAGE": "demo:build",
		"SEMREL_PLUGIN_REF":   "localhost:5000/team/demo:{version}",
		"SEMREL_NEXT_VERSION": "1.2.3+ci.7",
		"SEMREL_DRY_RUN":      "true",
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run(context.Background(), &stdout, &stderr, entrypointEnv(env), runner)

	require.Equal(t, 0, code)
	require.Equal(t, "plugin_schema_version=1\n", stderr.String())
	require.Equal(
		t,
		"publisher-docker: [dry-run] inspected demo:build; would create tag localhost:5000/team/demo:1.2.3_ci.7 and push it once\n",
		stdout.String(),
	)
	require.Len(t, runner.commands, 2)
	for _, command := range runner.commands {
		require.Equal(t, "inspect", command.Args[1])
	}
}

func TestRunValidationFailurePreservesHandshake(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run(context.Background(), &stdout, &stderr, entrypointEnv(map[string]string{
		"SEMREL_PLUGIN_IMAGE": "demo:build",
		"SEMREL_VERSION":      "1.2.3",
	}), &entrypointRunner{})

	require.Equal(t, 1, code)
	require.Empty(t, stdout.String())
	require.Equal(
		t,
		"plugin_schema_version=1\npublisher-docker: SEMREL_PLUGIN_REF is required\n",
		stderr.String(),
	)
	require.True(t, strings.HasPrefix(stderr.String(), "plugin_schema_version=1\n"))
}

func TestRunRedactsDockerDiagnosticsAndReturnsFailure(t *testing.T) {
	t.Parallel()

	runner := &entrypointRunner{responses: []entrypointResponse{{
		output: plugin.CommandOutput{Stderr: []byte("Cannot connect to the Docker daemon; password=top-secret")},
		err:    errors.New("top-secret"),
	}}}
	env := map[string]string{
		"SEMREL_PLUGIN_IMAGE": "demo:build",
		"SEMREL_PLUGIN_REF":   "localhost:5000/team/demo:{version}",
		"SEMREL_VERSION":      "1.2.3",
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run(context.Background(), &stdout, &stderr, entrypointEnv(env), runner)

	require.Equal(t, 1, code)
	require.Empty(t, stdout.String())
	require.Equal(
		t,
		"plugin_schema_version=1\npublisher-docker: inspect source image: Docker daemon is unavailable\n",
		stderr.String(),
	)
	require.NotContains(t, stderr.String(), "top-secret")
}

func TestRunCanceledContextReturnsFailure(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run(ctx, &stdout, &stderr, entrypointEnv(map[string]string{
		"SEMREL_PLUGIN_IMAGE": "demo:build",
		"SEMREL_PLUGIN_REF":   "localhost:5000/team/demo:{version}",
		"SEMREL_VERSION":      "1.2.3",
	}), &entrypointRunner{})

	require.Equal(t, 1, code)
	require.Equal(
		t,
		"plugin_schema_version=1\npublisher-docker: operation canceled\n",
		stderr.String(),
	)
	require.Empty(t, stdout.String())
}

func entrypointEnv(values map[string]string) func(string) string {
	return func(key string) string {
		return values[key]
	}
}

func entrypointInspect(id string) plugin.CommandOutput {
	return plugin.CommandOutput{
		Stdout: []byte(`[{"Id":"` + id + `","RepoTags":[],"RepoDigests":null}]`),
	}
}
