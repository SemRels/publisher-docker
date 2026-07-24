//go:build integration && linux

// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The publisher-docker Authors

package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	plugin "github.com/SemRels/publisher-docker/internal/plugin"
	"github.com/distribution/reference"
)

func TestIntegrationPublishesLocalImageToRegistry(t *testing.T) {
	registry := os.Getenv("SEMREL_TEST_DOCKER_REGISTRY")
	if registry == "" {
		t.Fatal("SEMREL_TEST_DOCKER_REGISTRY is required when integration tests are selected")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("Docker CLI is required when integration tests are selected: %v", err)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	registryURL := "http://" + registry + "/v2/"
	response, err := client.Get(registryURL)
	if err != nil {
		t.Fatalf("local registry must be reachable at %s: %v", registryURL, err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("local registry status = %d, want 200", response.StatusCode)
	}

	suffix := strconv.FormatInt(time.Now().UnixNano(), 36) + "-" + strconv.Itoa(os.Getpid())
	source := "semrel-publisher-docker-integration:" + suffix
	repository := "semrel-integration/publisher-docker/" + suffix
	refTemplate := registry + "/" + repository + ":{version}"
	destination := registry + "/" + repository + ":1.2.3_integration.1"
	dryDestination := registry + "/" + repository + ":9.9.9_dry-run"
	t.Cleanup(func() {
		_ = dockerCommand("image", "rm", "--force", source, destination, dryDestination)
	})

	contextDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(contextDir, "payload.txt"), []byte("publisher-docker integration\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dockerfile := "FROM scratch\nCOPY payload.txt /payload.txt\n"
	if err := os.WriteFile(filepath.Join(contextDir, "Dockerfile"), []byte(dockerfile), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := dockerCommand("image", "build", "--tag", source, contextDir); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(context.Background(), &stdout, &stderr, entrypointEnv(map[string]string{
		"SEMREL_PLUGIN_IMAGE": source,
		"SEMREL_PLUGIN_REF":   refTemplate,
		"SEMREL_VERSION":      "v1.2.3+integration.1",
	}), plugin.ExecRunner{})
	if exitCode != 0 {
		t.Fatalf("publish exit code = %d, stdout = %q, stderr = %q", exitCode, stdout.String(), stderr.String())
	}

	immutableRef := strings.TrimSpace(strings.TrimPrefix(
		stdout.String(),
		"publisher-docker: published "+destination+" as ",
	))
	parsed, err := reference.ParseNormalizedNamed(immutableRef)
	if err != nil {
		t.Fatalf("reported immutable reference %q is invalid: %v", immutableRef, err)
	}
	canonical, ok := parsed.(reference.Canonical)
	if !ok {
		t.Fatalf("reported reference %q is not immutable", immutableRef)
	}

	remoteDigest, status := remoteManifestDigest(t, client, registry, repository, "1.2.3_integration.1")
	if status != http.StatusOK {
		t.Fatalf("published manifest status = %d, want 200", status)
	}
	if remoteDigest != canonical.Digest().String() {
		t.Fatalf("remote Docker-Content-Digest = %q, reported digest = %q", remoteDigest, canonical.Digest())
	}

	stdout.Reset()
	stderr.Reset()
	exitCode = run(context.Background(), &stdout, &stderr, entrypointEnv(map[string]string{
		"SEMREL_PLUGIN_IMAGE": source,
		"SEMREL_PLUGIN_REF":   refTemplate,
		"SEMREL_VERSION":      "9.9.9+dry-run",
		"SEMREL_DRY_RUN":      "true",
	}), plugin.ExecRunner{})
	if exitCode != 0 {
		t.Fatalf("dry-run exit code = %d, stdout = %q, stderr = %q", exitCode, stdout.String(), stderr.String())
	}
	if command := exec.Command("docker", "image", "inspect", dryDestination); command.Run() == nil {
		t.Fatalf("dry-run created local destination tag %s", dryDestination)
	}
	if _, status := remoteManifestDigest(t, client, registry, repository, "9.9.9_dry-run"); status != http.StatusNotFound {
		t.Fatalf("dry-run remote manifest status = %d, want 404", status)
	}

	if err := dockerCommand("image", "rm", "--force", source, destination); err != nil {
		t.Fatal(err)
	}
	if err := dockerCommand("image", "pull", destination); err != nil {
		t.Fatal(err)
	}
	inspect := exec.Command("docker", "image", "inspect", "--format", "{{.Id}}", destination)
	inspectOutput, err := inspect.CombinedOutput()
	if err != nil {
		t.Fatalf("inspect pulled image: %v: %s", err, inspectOutput)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(inspectOutput)), "sha256:") {
		t.Fatalf("pulled image ID = %q, want sha256 digest", inspectOutput)
	}
}

func remoteManifestDigest(
	t *testing.T,
	client *http.Client,
	registry, repository, tag string,
) (string, int) {
	t.Helper()
	url := fmt.Sprintf("http://%s/v2/%s/manifests/%s", registry, repository, tag)
	request, err := http.NewRequestWithContext(context.Background(), http.MethodHead, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set(
		"Accept",
		"application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json, application/vnd.oci.image.index.v1+json",
	)
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("request manifest %s: %v", tag, err)
	}
	_ = response.Body.Close()
	return response.Header.Get("Docker-Content-Digest"), response.StatusCode
}

func dockerCommand(args ...string) error {
	command := exec.Command("docker", args...)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, output)
	}
	return nil
}
