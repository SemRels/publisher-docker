// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The publisher-docker Authors

package plugin

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	testSourceID       = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testOtherID        = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testManifestDigest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

type runnerStep struct {
	command Command
	output  CommandOutput
	err     error
}

type stepRunner struct {
	t        *testing.T
	steps    []runnerStep
	commands []Command
}

type runnerFunc func(context.Context, Command) (CommandOutput, error)

func (run runnerFunc) Run(ctx context.Context, command Command) (CommandOutput, error) {
	return run(ctx, command)
}

func (r *stepRunner) Run(_ context.Context, command Command) (CommandOutput, error) {
	r.t.Helper()
	r.commands = append(r.commands, command)
	if len(r.steps) == 0 {
		r.t.Fatalf("unexpected command: %#v", command)
	}
	step := r.steps[0]
	r.steps = r.steps[1:]
	require.Equal(r.t, step.command, command)
	return step.output, step.err
}

func (r *stepRunner) done() {
	r.t.Helper()
	require.Empty(r.t, r.steps, "not all expected commands ran")
}

func TestConfigFromEnv(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		env             map[string]string
		wantVersion     string
		wantDestination string
		wantDryRun      bool
	}{
		{
			name: "version strips one v and encodes build metadata",
			env: map[string]string{
				"SEMREL_PLUGIN_IMAGE": "local/image:build",
				"SEMREL_PLUGIN_REF":   "ghcr.io/semrels/demo:{version}",
				"SEMREL_VERSION":      "v1.2.3+build.7",
			},
			wantVersion:     "1.2.3_build.7",
			wantDestination: "ghcr.io/semrels/demo:1.2.3_build.7",
		},
		{
			name: "next version fallback and registry port",
			env: map[string]string{
				"SEMREL_PLUGIN_IMAGE": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"SEMREL_PLUGIN_REF":   "localhost:5000/team/demo:{version}",
				"SEMREL_NEXT_VERSION": "2.0.0-rc.1",
				"SEMREL_DRY_RUN":      "1",
			},
			wantVersion:     "2.0.0-rc.1",
			wantDestination: "localhost:5000/team/demo:2.0.0-rc.1",
			wantDryRun:      true,
		},
		{
			name: "only one leading v is removed",
			env: map[string]string{
				"SEMREL_PLUGIN_IMAGE": "demo",
				"SEMREL_PLUGIN_REF":   "example.com/demo:{version}",
				"SEMREL_VERSION":      "vv1",
				"SEMREL_DRY_RUN":      "false",
			},
			wantVersion:     "v1",
			wantDestination: "example.com/demo:v1",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			config, err := ConfigFromEnv(mapEnv(test.env))
			require.NoError(t, err)
			require.Equal(t, test.env["SEMREL_PLUGIN_IMAGE"], config.Image)
			require.Equal(t, test.wantVersion, config.Version)
			require.Equal(t, test.wantDestination, config.Destination)
			require.Equal(t, test.wantDryRun, config.DryRun)
		})
	}
}

func TestConfigFromEnvRejectsInvalidValues(t *testing.T) {
	t.Parallel()

	valid := map[string]string{
		"SEMREL_PLUGIN_IMAGE": "local/image:build",
		"SEMREL_PLUGIN_REF":   "ghcr.io/semrels/demo:{version}",
		"SEMREL_VERSION":      "1.2.3",
	}
	digestDestination := "ghcr.io/semrels/demo:{version}@" + testManifestDigest
	tests := []struct {
		name    string
		mutate  func(map[string]string)
		wantErr string
	}{
		{name: "missing image", mutate: func(env map[string]string) { delete(env, "SEMREL_PLUGIN_IMAGE") }, wantErr: "SEMREL_PLUGIN_IMAGE is required"},
		{name: "empty image", mutate: func(env map[string]string) { env["SEMREL_PLUGIN_IMAGE"] = "   " }, wantErr: "SEMREL_PLUGIN_IMAGE is required"},
		{name: "image control character", mutate: func(env map[string]string) { env["SEMREL_PLUGIN_IMAGE"] += "\r\nsecret" }, wantErr: "SEMREL_PLUGIN_IMAGE must not contain control characters"},
		{name: "invalid image", mutate: func(env map[string]string) { env["SEMREL_PLUGIN_IMAGE"] = "ghcr.io/SemRels/image:tag" }, wantErr: "SEMREL_PLUGIN_IMAGE is not a valid Docker image reference or image ID"},
		{name: "missing ref", mutate: func(env map[string]string) { delete(env, "SEMREL_PLUGIN_REF") }, wantErr: "SEMREL_PLUGIN_REF is required"},
		{name: "ref control character", mutate: func(env map[string]string) { env["SEMREL_PLUGIN_REF"] += "\n" }, wantErr: "SEMREL_PLUGIN_REF must not contain control characters"},
		{name: "placeholder required", mutate: func(env map[string]string) { env["SEMREL_PLUGIN_REF"] = "ghcr.io/semrels/demo:latest" }, wantErr: "SEMREL_PLUGIN_REF must contain {version}"},
		{name: "unresolved placeholder", mutate: func(env map[string]string) { env["SEMREL_PLUGIN_REF"] = "ghcr.io/semrels/demo:{version}-{channel}" }, wantErr: "SEMREL_PLUGIN_REF contains an unresolved placeholder"},
		{name: "digest destination", mutate: func(env map[string]string) { env["SEMREL_PLUGIN_REF"] = digestDestination }, wantErr: "resolved SEMREL_PLUGIN_REF must not be a digest destination"},
		{name: "invalid destination", mutate: func(env map[string]string) { env["SEMREL_PLUGIN_REF"] = "ghcr.io/SemRels/demo:{version}" }, wantErr: "resolved SEMREL_PLUGIN_REF is not a valid Docker reference"},
		{name: "destination needs explicit tag", mutate: func(env map[string]string) { env["SEMREL_PLUGIN_REF"] = "ghcr.io/semrels/{version}" }, wantErr: "resolved SEMREL_PLUGIN_REF must include an explicit tag"},
		{name: "missing version", mutate: func(env map[string]string) { delete(env, "SEMREL_VERSION") }, wantErr: "SEMREL_VERSION or SEMREL_NEXT_VERSION is required"},
		{name: "version control character", mutate: func(env map[string]string) { env["SEMREL_VERSION"] += "\rsecret" }, wantErr: "SEMREL_VERSION must not contain control characters"},
		{name: "empty normalized version", mutate: func(env map[string]string) { env["SEMREL_VERSION"] = "v" }, wantErr: "release version is empty after removing the leading v"},
		{name: "invalid dry run", mutate: func(env map[string]string) { env["SEMREL_DRY_RUN"] = "yes" }, wantErr: "SEMREL_DRY_RUN must be one of true, false, 1, or 0"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			env := cloneMap(valid)
			test.mutate(env)
			config, err := ConfigFromEnv(mapEnv(env))
			require.EqualError(t, err, test.wantErr)
			require.Equal(t, Config{}, config)
		})
	}
}

func TestBuildPlanChoosesTagAction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		destinationStep   runnerStep
		wantAction        TagAction
		wantDestinationID string
	}{
		{
			name: "create missing destination",
			destinationStep: runnerStep{
				command: dockerCommand("image", "inspect", "localhost:5000/demo:1.2.3"),
				output:  CommandOutput{Stderr: []byte("Error: No such image: localhost:5000/demo:1.2.3")},
				err:     errors.New("exit status 1"),
			},
			wantAction: TagCreate,
		},
		{
			name: "skip matching destination",
			destinationStep: runnerStep{
				command: dockerCommand("image", "inspect", "localhost:5000/demo:1.2.3"),
				output:  inspectOutput(testSourceID, nil),
			},
			wantAction:        TagSkip,
			wantDestinationID: testSourceID,
		},
		{
			name: "replace different destination",
			destinationStep: runnerStep{
				command: dockerCommand("image", "inspect", "localhost:5000/demo:1.2.3"),
				output:  inspectOutput(testOtherID, nil),
			},
			wantAction:        TagReplace,
			wantDestinationID: testOtherID,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runner := &stepRunner{t: t, steps: []runnerStep{
				{
					command: dockerCommand("image", "inspect", "demo:build"),
					output:  inspectOutput(testSourceID, nil),
				},
				test.destinationStep,
			}}
			plan, err := BuildPlan(context.Background(), Config{
				Image:       "demo:build",
				Destination: "localhost:5000/demo:1.2.3",
				DryRun:      true,
			}, runner)
			require.NoError(t, err)
			require.Equal(t, test.wantAction, plan.TagAction)
			require.Equal(t, test.wantDestinationID, plan.DestinationID)
			require.Equal(t, testSourceID, plan.SourceID)
			runner.done()
		})
	}
}

func TestPublishTagsVerifiesAndPushesExactlyOnce(t *testing.T) {
	t.Parallel()

	destination := "localhost:5000/team/demo:1.2.3"
	runner := &stepRunner{t: t, steps: []runnerStep{
		{command: dockerCommand("image", "inspect", "demo:build"), output: inspectOutput(testSourceID, nil)},
		{
			command: dockerCommand("image", "inspect", destination),
			output:  CommandOutput{Stderr: []byte("Error: No such image")},
			err:     errors.New("exit status 1"),
		},
		{command: dockerCommand("image", "tag", "demo:build", destination)},
		{command: dockerCommand("image", "inspect", destination), output: inspectOutput(testSourceID, nil)},
		{
			command: dockerCommand("image", "push", destination),
			output:  CommandOutput{Stderr: []byte("latest: digest: " + testManifestDigest + " size: 742\n")},
		},
	}}

	result, err := Publish(context.Background(), Config{
		Image:       "demo:build",
		Destination: destination,
	}, runner)
	require.NoError(t, err)
	require.True(t, result.Published)
	require.Equal(t, testManifestDigest, result.Digest)
	require.Equal(t, "localhost:5000/team/demo@"+testManifestDigest, result.ImmutableRef)
	require.Equal(t, TagCreate, result.Plan.TagAction)
	require.Equal(t, 1, countCommands(runner.commands, "push"))
	runner.done()
}

func TestPublishSkipsExistingTagAndUsesRepoDigestFallback(t *testing.T) {
	t.Parallel()

	destination := "localhost:5000/team/demo:1.2.3"
	repoDigest := "localhost:5000/team/demo@" + testManifestDigest
	runner := &stepRunner{t: t, steps: []runnerStep{
		{command: dockerCommand("image", "inspect", "demo:build"), output: inspectOutput(testSourceID, nil)},
		{command: dockerCommand("image", "inspect", destination), output: inspectOutput(testSourceID, nil)},
		{command: dockerCommand("image", "inspect", destination), output: inspectOutput(testSourceID, nil)},
		{command: dockerCommand("image", "push", destination), output: CommandOutput{Stdout: []byte("pushed\n")}},
		{
			command: dockerCommand("image", "inspect", destination),
			output: inspectOutput(testSourceID, []string{
				"example.com/unrelated/demo@" + testOtherID,
				repoDigest,
			}),
		},
	}}

	result, err := Publish(context.Background(), Config{
		Image:       "demo:build",
		Destination: destination,
	}, runner)
	require.NoError(t, err)
	require.Equal(t, TagSkip, result.Plan.TagAction)
	require.Equal(t, repoDigest, result.ImmutableRef)
	require.Equal(t, 0, countCommands(runner.commands, "tag"))
	require.Equal(t, 1, countCommands(runner.commands, "push"))
	runner.done()
}

func TestPublishDryRunOnlyInspects(t *testing.T) {
	t.Parallel()

	destination := "localhost:5000/team/demo:1.2.3"
	runner := &stepRunner{t: t, steps: []runnerStep{
		{command: dockerCommand("image", "inspect", "demo:build"), output: inspectOutput(testSourceID, nil)},
		{
			command: dockerCommand("image", "inspect", destination),
			output:  CommandOutput{Stderr: []byte("Error: No such image")},
			err:     errors.New("exit status 1"),
		},
	}}

	result, err := Publish(context.Background(), Config{
		Image:       "demo:build",
		Destination: destination,
		DryRun:      true,
	}, runner)
	require.NoError(t, err)
	require.False(t, result.Published)
	require.Equal(t, TagCreate, result.Plan.TagAction)
	require.Len(t, runner.commands, 2)
	for _, command := range runner.commands {
		require.Equal(t, []string{"image", "inspect"}, command.Args[:2])
	}
	runner.done()
}

func TestPublishReportsSanitizedDockerFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		steps     []runnerStep
		wantError string
		wantKind  DockerErrorKind
	}{
		{
			name: "Docker CLI missing",
			steps: []runnerStep{{
				command: dockerCommand("image", "inspect", "demo:build"),
				err:     exec.ErrNotFound,
			}},
			wantError: `Docker CLI "docker" was not found on PATH`,
			wantKind:  DockerErrorCLI,
		},
		{
			name: "daemon unavailable",
			steps: []runnerStep{{
				command: dockerCommand("image", "inspect", "demo:build"),
				output:  CommandOutput{Stderr: []byte("Cannot connect to the Docker daemon. token=daemon-secret")},
				err:     errors.New("daemon-secret"),
			}},
			wantError: "inspect source image: Docker daemon is unavailable",
			wantKind:  DockerErrorDaemon,
		},
		{
			name: "source missing",
			steps: []runnerStep{{
				command: dockerCommand("image", "inspect", "demo:build"),
				output:  CommandOutput{Stderr: []byte("No such image: demo:build")},
				err:     errors.New("exit status 1"),
			}},
			wantError: "inspect source image: image was not found",
			wantKind:  DockerErrorMissing,
		},
		{
			name: "destination inspect daemon failure",
			steps: []runnerStep{
				{command: dockerCommand("image", "inspect", "demo:build"), output: inspectOutput(testSourceID, nil)},
				{
					command: dockerCommand("image", "inspect", "localhost:5000/demo:1.2.3"),
					output:  CommandOutput{Stderr: []byte("error during connect: secret")},
					err:     errors.New("secret"),
				},
			},
			wantError: "inspect destination image: Docker daemon is unavailable",
			wantKind:  DockerErrorDaemon,
		},
		{
			name: "tag failure",
			steps: []runnerStep{
				{command: dockerCommand("image", "inspect", "demo:build"), output: inspectOutput(testSourceID, nil)},
				{
					command: dockerCommand("image", "inspect", "localhost:5000/demo:1.2.3"),
					output:  CommandOutput{Stderr: []byte("No such image")},
					err:     errors.New("exit status 1"),
				},
				{
					command: dockerCommand("image", "tag", "demo:build", "localhost:5000/demo:1.2.3"),
					output:  CommandOutput{Stderr: []byte("tag failed secret-value")},
					err:     errors.New("secret-value"),
				},
			},
			wantError: "tag destination image: Docker command failed",
			wantKind:  DockerErrorCommand,
		},
		{
			name: "push authentication failure",
			steps: []runnerStep{
				{command: dockerCommand("image", "inspect", "demo:build"), output: inspectOutput(testSourceID, nil)},
				{command: dockerCommand("image", "inspect", "localhost:5000/demo:1.2.3"), output: inspectOutput(testSourceID, nil)},
				{command: dockerCommand("image", "inspect", "localhost:5000/demo:1.2.3"), output: inspectOutput(testSourceID, nil)},
				{
					command: dockerCommand("image", "push", "localhost:5000/demo:1.2.3"),
					output:  CommandOutput{Stderr: []byte("unauthorized: token=super-secret")},
					err:     errors.New("super-secret"),
				},
			},
			wantError: "push destination image: Docker authentication or authorization failed",
			wantKind:  DockerErrorAuth,
		},
		{
			name: "push command failure",
			steps: []runnerStep{
				{command: dockerCommand("image", "inspect", "demo:build"), output: inspectOutput(testSourceID, nil)},
				{command: dockerCommand("image", "inspect", "localhost:5000/demo:1.2.3"), output: inspectOutput(testSourceID, nil)},
				{command: dockerCommand("image", "inspect", "localhost:5000/demo:1.2.3"), output: inspectOutput(testSourceID, nil)},
				{
					command: dockerCommand("image", "push", "localhost:5000/demo:1.2.3"),
					output:  CommandOutput{Stderr: []byte("manifest commit failed: registry-secret")},
					err:     errors.New("registry-secret"),
				},
			},
			wantError: "push destination image: Docker command failed",
			wantKind:  DockerErrorCommand,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runner := &stepRunner{t: t, steps: test.steps}
			_, err := Publish(context.Background(), Config{
				Image:       "demo:build",
				Destination: "localhost:5000/demo:1.2.3",
			}, runner)
			require.EqualError(t, err, test.wantError)
			var dockerErr *DockerError
			require.ErrorAs(t, err, &dockerErr)
			require.Equal(t, test.wantKind, dockerErr.Kind)
			require.NotContains(t, err.Error(), "secret")
			runner.done()
		})
	}
}

func TestPublishRejectsUntrustworthyDigest(t *testing.T) {
	t.Parallel()

	destination := "localhost:5000/team/demo:1.2.3"
	runner := &stepRunner{t: t, steps: []runnerStep{
		{command: dockerCommand("image", "inspect", "demo:build"), output: inspectOutput(testSourceID, nil)},
		{command: dockerCommand("image", "inspect", destination), output: inspectOutput(testSourceID, nil)},
		{command: dockerCommand("image", "inspect", destination), output: inspectOutput(testSourceID, nil)},
		{
			command: dockerCommand("image", "push", destination),
			output:  CommandOutput{Stdout: []byte("digest: sha256:not-a-digest\n")},
		},
		{
			command: dockerCommand("image", "inspect", destination),
			output:  inspectOutput(testSourceID, []string{"example.com/other/demo@" + testManifestDigest}),
		},
	}}

	_, err := Publish(context.Background(), Config{Image: "demo:build", Destination: destination}, runner)
	require.EqualError(t, err, "docker push succeeded but no trustworthy sha256 manifest digest was reported")
	require.Equal(t, 1, countCommands(runner.commands, "push"))
	runner.done()
}

func TestPublishDetectsVerificationMismatch(t *testing.T) {
	t.Parallel()

	destination := "localhost:5000/team/demo:1.2.3"
	runner := &stepRunner{t: t, steps: []runnerStep{
		{command: dockerCommand("image", "inspect", "demo:build"), output: inspectOutput(testSourceID, nil)},
		{command: dockerCommand("image", "inspect", destination), output: inspectOutput(testOtherID, nil)},
		{command: dockerCommand("image", "tag", "demo:build", destination)},
		{command: dockerCommand("image", "inspect", destination), output: inspectOutput(testOtherID, nil)},
	}}

	_, err := Publish(context.Background(), Config{Image: "demo:build", Destination: destination}, runner)
	require.EqualError(t, err, "verify destination image: destination does not identify the source image")
	require.Equal(t, 0, countCommands(runner.commands, "push"))
	runner.done()
}

func TestPublishHonorsCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runner := &stepRunner{t: t}
	_, err := Publish(ctx, Config{Image: "demo:build", Destination: "example.com/demo:1.2.3"}, runner)
	require.EqualError(t, err, "operation canceled")
	require.Empty(t, runner.commands)
}

func TestPublishHonorsCancellationDuringDockerCommand(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	runner := runnerFunc(func(context.Context, Command) (CommandOutput, error) {
		cancel()
		return CommandOutput{Stderr: []byte("sensitive daemon output")}, errors.New("sensitive error")
	})

	_, err := Publish(ctx, Config{Image: "demo:build", Destination: "example.com/demo:1.2.3"}, runner)
	require.EqualError(t, err, "operation canceled")
	require.NotContains(t, err.Error(), "sensitive")
}

func TestDigestFromPush(t *testing.T) {
	t.Parallel()

	require.Equal(t, testManifestDigest, digestFromPush(CommandOutput{
		Stdout: []byte("layer: pushed\n1.2.3: digest: " + testManifestDigest + " size: 123\n"),
	}))
	require.Equal(t, testManifestDigest, digestFromPush(CommandOutput{
		Stdout: []byte("localhost:5000/demo@" + testManifestDigest + "\n"),
	}))
	require.Empty(t, digestFromPush(CommandOutput{
		Stdout: []byte("layer sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd pushed\n"),
	}))
}

func mapEnv(values map[string]string) func(string) string {
	return func(key string) string {
		return values[key]
	}
}

func cloneMap(values map[string]string) map[string]string {
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func inspectOutput(id string, repoDigests []string) CommandOutput {
	digests := "null"
	if repoDigests != nil {
		quoted := make([]string, 0, len(repoDigests))
		for _, value := range repoDigests {
			quoted = append(quoted, `"`+value+`"`)
		}
		digests = "[" + strings.Join(quoted, ",") + "]"
	}
	return CommandOutput{Stdout: []byte(`[{"Id":"` + id + `","RepoTags":[],"RepoDigests":` + digests + `}]`)}
}

func countCommands(commands []Command, subcommand string) int {
	count := 0
	for _, command := range commands {
		if len(command.Args) >= 2 && command.Args[1] == subcommand {
			count++
		}
	}
	return count
}
