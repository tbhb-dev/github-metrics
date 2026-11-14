// SPDX-License-Identifier: Apache-2.0
// Copyright Tony Burns

package deploy_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

const (
	digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	repo   = "ghcr.io/tbhb-dev/github-metrics"
	pull   = "--pull"
)

// result is what one run of up.sh left behind.
type result struct {
	output string
	record string
	code   int
	ran    bool
}

// runUp runs up.sh with the given arguments. PATH lists one directory, which
// has a stub docker and links to the tools the script calls. The locale is
// UTF-8, which the script has to override, so the host's shell settings can't
// change the outcome. The stub records its arguments and the image variable in
// a file, which shows whether the script got as far as running Compose.
func runUp(t *testing.T, args ...string) result {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("the script needs a POSIX shell")
	}

	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	stub := "#!/bin/sh\n{ echo \"$GITHUB_METRICS_IMAGE\"; printf '%s\\n' \"$@\"; } > '" + record + "'\n"

	err := os.WriteFile(filepath.Join(dir, "docker"), []byte(stub), 0o700) //nolint:gosec // the stub must be executable
	if err != nil {
		t.Fatal(err)
	}

	// The script may call only these tools, so a new one is added here and in
	// the script's header on purpose.
	for _, tool := range []string{"grep", "dirname"} {
		path, lookErr := exec.LookPath(tool)
		if lookErr != nil {
			t.Fatal(lookErr)
		}

		linkErr := os.Symlink(path, filepath.Join(dir, tool))
		if linkErr != nil {
			t.Fatal(linkErr)
		}
	}

	argv := append([]string{"up.sh"}, args...)
	cmd := exec.CommandContext(t.Context(), "/bin/sh", argv...)
	cmd.Env = []string{"PATH=" + dir, "LC_ALL=en_US.UTF-8"}

	out, err := cmd.CombinedOutput()
	res := result{output: string(out), record: "", code: 0, ran: false}

	var exitErr *exec.ExitError

	switch {
	case errors.As(err, &exitErr):
		res.code = exitErr.ExitCode()
	case err != nil:
		t.Fatal(err)
	}

	got, readErr := os.ReadFile(record)
	if readErr == nil {
		res.ran = true
		res.record = string(got)
	}

	return res
}

// checkRan runs up.sh and expects it to hand docker the image in the
// environment and exactly the Compose arguments, one per line.
func checkRan(t *testing.T, image string, args ...string) {
	t.Helper()

	res := runUp(t, append([]string{image}, args...)...)

	if res.code != 0 || !res.ran {
		t.Fatalf("up.sh %s: exit %d, ran=%v: %s", image, res.code, res.ran, res.output)
	}

	want := append([]string{image, "compose", "-f", "./compose.yaml", "up", "-d"}, args...)

	if got := strings.Split(strings.TrimSuffix(res.record, "\n"), "\n"); !slices.Equal(got, want) {
		t.Errorf("docker saw %q, want %q", got, want)
	}
}

func TestUpAcceptsDigest(t *testing.T) {
	t.Parallel()

	checkRan(t, repo+"@"+digest)
}

// TestUpAcceptsImageID covers the form a classic image store needs.
func TestUpAcceptsImageID(t *testing.T) {
	t.Parallel()

	checkRan(t, digest, pull, "never")
}

func TestUpRefusesOtherArguments(t *testing.T) {
	t.Parallel()

	image := repo + "@" + digest
	argSets := map[string][]string{
		"other compose file": {"-f", "other.yaml"},
		"project name":       {"-p", "other"},
		"env file":           {"--env-file", "other.env"},
		"pull always":        {pull, "always"},
		"pull missing value": {pull},
		"pull joined":        {"--pull=never"},
		"pull in one word":   {"--pull never"},
		"extra after pull":   {pull, "never", "--force-recreate"},
		"extra option":       {"--wait"},
	}

	for name, args := range argSets {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			res := runUp(t, append([]string{image}, args...)...)

			if res.code != 2 || res.ran {
				t.Errorf("up.sh %q: exit %d, ran=%v, want exit 2 and no run: %s", args, res.code, res.ran, res.output)
			}
		})
	}
}

func TestUpRefusesOtherReferences(t *testing.T) {
	t.Parallel()

	refs := map[string]string{
		"empty":            "",
		"tag":              repo + ":sha-abc",
		"latest":           repo + ":latest",
		"tag and digest":   repo + ":v1@" + digest,
		"leading dash":     "-" + repo + "@" + digest,
		"registry port":    "localhost:5000/github-metrics@" + digest,
		"short digest":     repo + "@sha256:abc",
		"upper digest":     repo + "@sha256:" + strings.Repeat("A", 64),
		"other algorithm":  repo + "@sha512:" + strings.Repeat("a", 64),
		"no name":          "@" + digest,
		"trailing text":    repo + "@" + digest + "x",
		"space":            repo + " @" + digest,
		"embedded newline": repo + ":latest\n" + repo + "@" + digest,
		"trailing newline": repo + "@" + digest + "\n",
		"carriage return":  repo + "@" + digest + "\r",
		"tab":              repo + "\t@" + digest,
		"vertical tab":     repo + "\v@" + digest,
		"form feed":        repo + "\f@" + digest,
		"trailing slash":   repo + "/@" + digest,
		"empty component":  "ghcr.io//github-metrics@" + digest,
		"dot component":    "ghcr.io/./github-metrics@" + digest,
		"parent component": "ghcr.io/../github-metrics@" + digest,
		"leading dot":      ".github-metrics@" + digest,
		"trailing dot":     "github-metrics.@" + digest,
		"double dot":       "github..metrics@" + digest,
		"trailing dash":    "github-metrics-@" + digest,
		"leading slash":    "/github-metrics@" + digest,
		"uppercase name":   "GHCR.io/tbhb-dev/github-metrics@" + digest,
		"non-ASCII name":   repo + string(rune(0xe9)) + "@" + digest,
		"id short":         "sha256:abc",
		"id upper":         "sha256:" + strings.Repeat("A", 64),
		"id not hex":       "sha256:" + strings.Repeat("g", 64),
		"id too long":      digest + "0",
		"id no prefix":     strings.TrimPrefix(digest, "sha256:"),
		"id empty":         "sha256:",
		"id glued to name": "github-metrics" + digest,
		"id with newline":  digest + "\n",
	}

	for name, image := range refs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			res := runUp(t, image)

			if res.code != 2 {
				t.Errorf("up.sh %q: exit %d, want 2: %s", image, res.code, res.output)
			}

			if res.ran {
				t.Errorf("up.sh %q ran docker: %q", image, res.record)
			}
		})
	}
}

func TestUpRefusesNoArgument(t *testing.T) {
	t.Parallel()

	res := runUp(t)

	if res.code != 2 || res.ran {
		t.Errorf("up.sh with no argument: exit %d, ran=%v, want exit 2 and no run: %s", res.code, res.ran, res.output)
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
