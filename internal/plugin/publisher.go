// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The publisher-docker Authors

package plugin

import (
	"bytes"
	"context"
	_ "crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"unicode"

	"github.com/distribution/reference"
	"github.com/opencontainers/go-digest"
)

const dockerExecutable = "docker"

type Config struct {
	Image       string
	RefTemplate string
	Version     string
	Destination string
	DryRun      bool
}

type TagAction string

const (
	TagCreate  TagAction = "create"
	TagReplace TagAction = "replace"
	TagSkip    TagAction = "skip"
)

type Plan struct {
	Source        string
	Destination   string
	SourceID      string
	DestinationID string
	TagAction     TagAction
	DryRun        bool
}

type Result struct {
	Plan         Plan
	Digest       string
	ImmutableRef string
	Published    bool
}

type Command struct {
	Name string
	Args []string
}

type CommandOutput struct {
	Stdout []byte
	Stderr []byte
}

type Runner interface {
	Run(context.Context, Command) (CommandOutput, error)
}

type ExecRunner struct{}

func (r ExecRunner) Run(ctx context.Context, command Command) (CommandOutput, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	cmd := exec.CommandContext(ctx, command.Name, command.Args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	output := CommandOutput{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return output, ctxErr
	}
	return output, err
}

type DockerErrorKind string

const (
	DockerErrorCLI     DockerErrorKind = "cli"
	DockerErrorDaemon  DockerErrorKind = "daemon"
	DockerErrorMissing DockerErrorKind = "image-not-found"
	DockerErrorAuth    DockerErrorKind = "authentication"
	DockerErrorCommand DockerErrorKind = "command"
)

type DockerError struct {
	Operation string
	Kind      DockerErrorKind
}

func (e *DockerError) Error() string {
	switch e.Kind {
	case DockerErrorCLI:
		return `Docker CLI "docker" was not found on PATH`
	case DockerErrorDaemon:
		return e.Operation + ": Docker daemon is unavailable"
	case DockerErrorMissing:
		return e.Operation + ": image was not found"
	case DockerErrorAuth:
		return e.Operation + ": Docker authentication or authorization failed"
	default:
		return e.Operation + ": Docker command failed"
	}
}

type imageInspect struct {
	ID          string   `json:"Id"`
	RepoTags    []string `json:"RepoTags"`
	RepoDigests []string `json:"RepoDigests"`
}

func ConfigFromEnv(getenv func(string) string) (Config, error) {
	if getenv == nil {
		return Config{}, errors.New("environment reader is required")
	}

	image, err := requiredValue(getenv, "SEMREL_PLUGIN_IMAGE")
	if err != nil {
		return Config{}, err
	}
	if _, err := reference.ParseAnyReference(image); err != nil {
		return Config{}, errors.New("SEMREL_PLUGIN_IMAGE is not a valid Docker image reference or image ID")
	}

	refTemplate, err := requiredValue(getenv, "SEMREL_PLUGIN_REF")
	if err != nil {
		return Config{}, err
	}
	if !strings.Contains(refTemplate, "{version}") {
		return Config{}, errors.New("SEMREL_PLUGIN_REF must contain {version}")
	}

	rawVersion := getenv("SEMREL_VERSION")
	versionVariable := "SEMREL_VERSION"
	if rawVersion == "" {
		rawVersion = getenv("SEMREL_NEXT_VERSION")
		versionVariable = "SEMREL_NEXT_VERSION"
	}
	if rawVersion == "" {
		return Config{}, errors.New("SEMREL_VERSION or SEMREL_NEXT_VERSION is required")
	}
	if err := validateValue(versionVariable, rawVersion); err != nil {
		return Config{}, err
	}
	version := strings.TrimPrefix(rawVersion, "v")
	version = strings.ReplaceAll(version, "+", "_")
	if version == "" {
		return Config{}, errors.New("release version is empty after removing the leading v")
	}

	destination, err := resolveDestination(refTemplate, version)
	if err != nil {
		return Config{}, err
	}

	dryRun, err := parseDryRun(getenv("SEMREL_DRY_RUN"))
	if err != nil {
		return Config{}, err
	}

	return Config{
		Image:       image,
		RefTemplate: refTemplate,
		Version:     version,
		Destination: destination,
		DryRun:      dryRun,
	}, nil
}

func BuildPlan(ctx context.Context, config Config, runner Runner) (Plan, error) {
	if runner == nil {
		return Plan{}, errors.New("docker command runner is required")
	}
	if err := contextError(ctx); err != nil {
		return Plan{}, err
	}

	source, err := inspectImage(ctx, runner, config.Image, "inspect source image")
	if err != nil {
		return Plan{}, err
	}
	if source.ID == "" {
		return Plan{}, errors.New("inspect source image: Docker returned an empty image ID")
	}

	destination, exists, err := inspectOptionalDestination(ctx, runner, config.Destination)
	if err != nil {
		return Plan{}, err
	}

	action := TagCreate
	destinationID := ""
	if exists {
		destinationID = destination.ID
		if destination.ID == source.ID {
			action = TagSkip
		} else {
			action = TagReplace
		}
	}

	return Plan{
		Source:        config.Image,
		Destination:   config.Destination,
		SourceID:      source.ID,
		DestinationID: destinationID,
		TagAction:     action,
		DryRun:        config.DryRun,
	}, nil
}

func Execute(ctx context.Context, plan Plan, runner Runner) (Result, error) {
	result := Result{Plan: plan}
	if runner == nil {
		return result, errors.New("docker command runner is required")
	}
	if err := contextError(ctx); err != nil {
		return result, err
	}
	if plan.DryRun {
		return result, nil
	}

	if plan.TagAction != TagSkip {
		output, err := runner.Run(ctx, dockerCommand("image", "tag", plan.Source, plan.Destination))
		if err != nil {
			return result, classifyDockerError(ctx, "tag destination image", output, err, false)
		}
	}

	verified, err := inspectImage(ctx, runner, plan.Destination, "verify destination image")
	if err != nil {
		return result, err
	}
	if verified.ID == "" || verified.ID != plan.SourceID {
		return result, errors.New("verify destination image: destination does not identify the source image")
	}

	pushOutput, err := runner.Run(ctx, dockerCommand("image", "push", plan.Destination))
	if err != nil {
		return result, classifyDockerError(ctx, "push destination image", pushOutput, err, false)
	}
	if err := contextError(ctx); err != nil {
		return result, err
	}

	repository, err := repositoryName(plan.Destination)
	if err != nil {
		return result, errors.New("resolve destination repository after push")
	}

	manifestDigest := digestFromPush(pushOutput)
	if manifestDigest == "" {
		afterPush, inspectErr := inspectImage(ctx, runner, plan.Destination, "inspect pushed image digest")
		if inspectErr != nil {
			return result, inspectErr
		}
		manifestDigest = digestFromRepoDigests(afterPush.RepoDigests, repository)
	}
	if manifestDigest == "" {
		return result, errors.New("docker push succeeded but no trustworthy sha256 manifest digest was reported")
	}

	result.Digest = manifestDigest
	result.ImmutableRef = repository + "@" + manifestDigest
	result.Published = true
	return result, nil
}

func Publish(ctx context.Context, config Config, runner Runner) (Result, error) {
	plan, err := BuildPlan(ctx, config, runner)
	if err != nil {
		return Result{}, err
	}
	return Execute(ctx, plan, runner)
}

func requiredValue(getenv func(string) string, name string) (string, error) {
	value := getenv(name)
	if value == "" || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	if err := validateValue(name, value); err != nil {
		return "", err
	}
	return value, nil
}

func validateValue(name, value string) error {
	for _, char := range value {
		if unicode.IsControl(char) {
			return fmt.Errorf("%s must not contain control characters", name)
		}
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("%s must not contain leading or trailing whitespace", name)
	}
	return nil
}

func parseDryRun(raw string) (bool, error) {
	if err := validateValue("SEMREL_DRY_RUN", raw); err != nil {
		return false, err
	}
	switch raw {
	case "", "false", "0":
		return false, nil
	case "true", "1":
		return true, nil
	default:
		return false, errors.New("SEMREL_DRY_RUN must be one of true, false, 1, or 0")
	}
}

func resolveDestination(refTemplate, version string) (string, error) {
	destination := strings.ReplaceAll(refTemplate, "{version}", version)
	if strings.ContainsAny(destination, "{}") {
		return "", errors.New("SEMREL_PLUGIN_REF contains an unresolved placeholder")
	}

	named, err := reference.ParseNormalizedNamed(destination)
	if err != nil {
		return "", errors.New("resolved SEMREL_PLUGIN_REF is not a valid Docker reference")
	}
	if _, isDigest := named.(reference.Canonical); isDigest {
		return "", errors.New("resolved SEMREL_PLUGIN_REF must not be a digest destination")
	}
	if _, isTagged := named.(reference.NamedTagged); !isTagged {
		return "", errors.New("resolved SEMREL_PLUGIN_REF must include an explicit tag")
	}
	return destination, nil
}

func inspectImage(ctx context.Context, runner Runner, image, operation string) (imageInspect, error) {
	output, err := runner.Run(ctx, dockerCommand("image", "inspect", image))
	if err != nil {
		return imageInspect{}, classifyDockerError(ctx, operation, output, err, true)
	}
	return decodeInspect(output.Stdout, operation)
}

func inspectOptionalDestination(ctx context.Context, runner Runner, image string) (imageInspect, bool, error) {
	output, err := runner.Run(ctx, dockerCommand("image", "inspect", image))
	if err != nil {
		if isImageNotFound(output) {
			return imageInspect{}, false, nil
		}
		return imageInspect{}, false, classifyDockerError(ctx, "inspect destination image", output, err, false)
	}
	inspect, decodeErr := decodeInspect(output.Stdout, "inspect destination image")
	if decodeErr != nil {
		return imageInspect{}, false, decodeErr
	}
	return inspect, true, nil
}

func decodeInspect(output []byte, operation string) (imageInspect, error) {
	var images []imageInspect
	if err := json.Unmarshal(output, &images); err != nil || len(images) != 1 {
		return imageInspect{}, fmt.Errorf("%s: Docker returned an invalid image inspection response", operation)
	}
	for _, value := range append(append([]string{images[0].ID}, images[0].RepoTags...), images[0].RepoDigests...) {
		if err := validateValue("Docker image inspection response", value); err != nil {
			return imageInspect{}, fmt.Errorf("%s: Docker returned an invalid image inspection response", operation)
		}
	}
	return images[0], nil
}

func dockerCommand(args ...string) Command {
	return Command{Name: dockerExecutable, Args: args}
}

func classifyDockerError(
	ctx context.Context,
	operation string,
	output CommandOutput,
	err error,
	missingIsError bool,
) error {
	if ctxErr := contextError(ctx); ctxErr != nil {
		return ctxErr
	}
	if errors.Is(err, exec.ErrNotFound) {
		return &DockerError{Operation: operation, Kind: DockerErrorCLI}
	}

	message := strings.ToLower(string(append(append([]byte{}, output.Stderr...), output.Stdout...)))
	switch {
	case containsAny(message,
		"cannot connect to the docker daemon",
		"is the docker daemon running",
		"error during connect",
		"open //./pipe/docker",
		"failed to connect to the docker api",
	):
		return &DockerError{Operation: operation, Kind: DockerErrorDaemon}
	case containsAny(message,
		"unauthorized",
		"authentication required",
		"no basic auth credentials",
		"requested access to the resource is denied",
		"access forbidden",
	):
		return &DockerError{Operation: operation, Kind: DockerErrorAuth}
	case missingIsError && isImageNotFound(output):
		return &DockerError{Operation: operation, Kind: DockerErrorMissing}
	default:
		return &DockerError{Operation: operation, Kind: DockerErrorCommand}
	}
}

func isImageNotFound(output CommandOutput) bool {
	message := strings.ToLower(string(append(append([]byte{}, output.Stderr...), output.Stdout...)))
	return containsAny(message, "no such image", "no such object", "image does not exist")
}

func containsAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return errors.New("operation deadline exceeded")
		}
		return errors.New("operation canceled")
	}
	return nil
}

func repositoryName(destination string) (string, error) {
	named, err := reference.ParseNormalizedNamed(destination)
	if err != nil {
		return "", err
	}
	return reference.TrimNamed(named).String(), nil
}

func digestFromPush(output CommandOutput) string {
	combined := append(append([]byte{}, output.Stdout...), output.Stderr...)
	for _, line := range strings.Split(string(combined), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		for index, field := range fields {
			if strings.EqualFold(strings.TrimSuffix(field, ":"), "digest") && index+1 < len(fields) {
				if parsed := parseSHA256Digest(fields[index+1]); parsed != "" {
					return parsed
				}
			}
		}
		if len(fields) == 1 {
			if parsed := parseSHA256Digest(fields[0]); parsed != "" {
				return parsed
			}
		}
	}
	return ""
}

func digestFromRepoDigests(repoDigests []string, repository string) string {
	for _, repoDigest := range repoDigests {
		named, err := reference.ParseNormalizedNamed(repoDigest)
		if err != nil {
			continue
		}
		canonical, ok := named.(reference.Canonical)
		if !ok || reference.TrimNamed(canonical).String() != repository {
			continue
		}
		if parsed := parseSHA256Digest(canonical.Digest().String()); parsed != "" {
			return parsed
		}
	}
	return ""
}

func parseSHA256Digest(value string) string {
	candidate := strings.Trim(value, " \t\r\n,;()[]<>\"'")
	if at := strings.LastIndex(candidate, "@"); at >= 0 {
		candidate = candidate[at+1:]
	}
	parsed, err := digest.Parse(candidate)
	if err != nil || parsed.Algorithm() != digest.SHA256 || parsed.Validate() != nil {
		return ""
	}
	return parsed.String()
}
