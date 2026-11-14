// SPDX-License-Identifier: Apache-2.0
// Copyright Tony Burns

package deploy_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// stubDocker writes a fake docker that records its arguments and the image
// variable in a file, so a test can see whether the script got as far as
// running Compose. It returns the stub's directory and the record's path.
func stubDocker(t *testing.T) (string, string) {
	t.Helper()

	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	stub := "#!/bin/sh\necho \"$GITHUB_METRICS_IMAGE|$*\" > '" + record + "'\n"

	err := os.WriteFile(filepath.Join(dir, "docker"), []byte(stub), 0o700) //nolint:gosec // the stub must be executable
	if err != nil {
		t.Fatal(err)
	}

	return dir, record
}

func runUp(t *testing.T, image string) (string, error) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("the script needs a POSIX shell")
	}

	dir, record := stubDocker(t)
	cmd := exec.CommandContext(t.Context(), "sh", "up.sh", image)

	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("up.sh: %w", err)
	}

	got, readErr := os.ReadFile(record)
	if readErr != nil {
		t.Fatal(readErr)
	}

	return string(got), nil
}

func TestUpAcceptsDigest(t *testing.T) {
	t.Parallel()

	image := "ghcr.io/tbhb-dev/github-metrics@" + digest

	got, err := runUp(t, image)
	if err != nil {
		t.Fatalf("up.sh %s: %v: %s", image, err, got)
	}

	if !strings.HasPrefix(got, image+"|compose -f ") ||
		!strings.HasSuffix(strings.TrimSpace(got), "compose.yaml up -d") {
		t.Errorf("docker saw %q", got)
	}
}

func TestUpRefusesOtherReferences(t *testing.T) {
	t.Parallel()

	refs := map[string]string{
		"empty":           "",
		"tag":             "ghcr.io/tbhb-dev/github-metrics:sha-abc",
		"latest":          "ghcr.io/tbhb-dev/github-metrics:latest",
		"trailing text":   "ghcr.io/tbhb-dev/github-metrics@" + digest + "x",
		"short digest":    "ghcr.io/tbhb-dev/github-metrics@sha256:abc",
		"upper digest":    "ghcr.io/tbhb-dev/github-metrics@sha256:" + strings.Repeat("A", 64),
		"other algorithm": "ghcr.io/tbhb-dev/github-metrics@sha512:" + strings.Repeat("a", 64),
		"no name":         "@" + digest,
		"space":           "ghcr.io/tbhb-dev/github-metrics @" + digest,
	}

	for name, image := range refs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := runUp(t, image)
			if err == nil {
				t.Errorf("up.sh %q succeeded and ran docker: %q", image, got)
			}
		})
	}
}

// TestComposeTakesTheImageVariable keeps the Compose file and the script
// agreed on the variable name.
func TestComposeTakesTheImageVariable(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("compose.yaml")
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(raw), "${GITHUB_METRICS_IMAGE:?") {
		t.Error("compose.yaml does not take its image from GITHUB_METRICS_IMAGE")
	}
}
