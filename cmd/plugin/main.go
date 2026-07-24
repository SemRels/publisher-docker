// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The publisher-docker Authors

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"

	plugin "github.com/SemRels/publisher-docker/internal/plugin"
)

const pluginSchemaVersion = 1

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Stdout, os.Stderr, os.Getenv, plugin.ExecRunner{}))
}

func run(
	ctx context.Context,
	stdout, stderr io.Writer,
	getenv func(string) string,
	runner plugin.Runner,
) int {
	_, _ = fmt.Fprintf(stderr, "plugin_schema_version=%d\n", pluginSchemaVersion)

	config, err := plugin.ConfigFromEnv(getenv)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "publisher-docker:", err)
		return 1
	}

	result, err := plugin.Publish(ctx, config, runner)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "publisher-docker:", err)
		return 1
	}

	if config.DryRun {
		switch result.Plan.TagAction {
		case plugin.TagSkip:
			_, _ = fmt.Fprintf(
				stdout,
				"publisher-docker: [dry-run] inspected %s; would keep %s and push it once\n",
				result.Plan.Source,
				result.Plan.Destination,
			)
		default:
			_, _ = fmt.Fprintf(
				stdout,
				"publisher-docker: [dry-run] inspected %s; would %s tag %s and push it once\n",
				result.Plan.Source,
				result.Plan.TagAction,
				result.Plan.Destination,
			)
		}
		return 0
	}

	_, _ = fmt.Fprintf(
		stdout,
		"publisher-docker: published %s as %s\n",
		result.Plan.Destination,
		result.ImmutableRef,
	)
	return 0
}
