// SPDX-License-Identifier: Apache-2.0
// Copyright Tony Burns

package deploy_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const (
	digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	repo   = "ghcr.io/tbhb-dev/github-metrics"
	pull   = "--pull"
	never  = "never"
)

// result is what one run of up.sh left behind.
type result struct {
	output string
	record string
	code   int
	ran    bool
}

// runUp runs up.sh under /bin/sh.
func runUp(t *testing.T, args ...string) result {
	t.Helper()

	return runIn(t, "/bin/sh", args...)
}

// runIn runs up.sh under the given shell with the given arguments. PATH lists
// one directory, which has a stub docker and links to the tools the script
// calls. The locale is
// UTF-8, which the script has to override, so the host's shell settings can't
// change the outcome. The stub records the image variable and each argument,
// every one ended by a zero byte, in a file, which shows whether the script got as
// far as running Compose.
func runIn(t *testing.T, shell string, args ...string) result {
	t.Helper()

	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	stub := "#!/bin/sh\nprintf '%s\\0' \"$GITHUB_METRICS_IMAGE\" \"$@\" > '" + record + "'\n"

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
	cmd := exec.CommandContext(t.Context(), shell, argv...)
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
// environment and exactly the Compose arguments.
func checkRan(t *testing.T, image string, args ...string) {
	t.Helper()

	checkRanIn(t, "/bin/sh", image, args...)
}

// checkRanIn expects the same under the given shell.
func checkRanIn(t *testing.T, shell, image string, args ...string) {
	t.Helper()

	res := runIn(t, shell, append([]string{image}, args...)...)

	if res.code != 0 || !res.ran {
		t.Fatalf("up.sh %s: exit %d, ran=%v: %s", image, res.code, res.ran, res.output)
	}

	want := append([]string{image, "compose", "-f", "./compose.yaml", "up", "-d"}, args...)

	if got := strings.Split(strings.TrimSuffix(res.record, "\x00"), "\x00"); !slices.Equal(got, want) {
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

	checkRan(t, digest, pull, never)
}

// refusedArgs lists what must not follow the image.
func refusedArgs() map[string][]string {
	return map[string][]string{
		"other compose file": {"-f", "other.yaml"},
		"project name":       {"-p", "other"},
		"env file":           {"--env-file", "other.env"},
		"pull always":        {pull, "always"},
		"pull missing value": {pull},
		"pull joined":        {"--pull=never"},
		"pull in one word":   {"--pull never"},
		"extra after pull":   {pull, never, "--force-recreate"},
		"extra option":       {"--wait"},
		"empty argument":     {""},
		"empty pull value":   {pull, ""},
		"repeated pair":      {pull, never, pull, never},
		"glob":               {"*"},
		"double dash":        {"--"},
		"double dash first":  {"--", pull, never},
		"negation":           {"!"},
		"negated pair":       {"!", never},
		"test option":        {"-n"},
		"test paren":         {"("},
		"test and":           {"-a"},
	}
}

func TestUpRefusesOtherArguments(t *testing.T) {
	t.Parallel()

	image := repo + "@" + digest

	for name, args := range refusedArgs() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			checkRefused(t, "/bin/sh", append([]string{image}, args...)...)
		})
	}
}

// checkRefused runs up.sh and expects it to exit 2 before it starts Compose.
func checkRefused(t *testing.T, shell string, args ...string) {
	t.Helper()

	res := runIn(t, shell, args...)

	if res.code != 2 || res.ran {
		t.Errorf("%s up.sh %q: exit %d, ran=%v, want exit 2 and no run: %s", shell, args, res.code, res.ran, res.output)
	}
}

// TestUpUnderOtherShells runs the same checks under the shells a deploy host
// may use as /bin/sh. A shell that isn't installed is skipped.
func TestUpUnderOtherShells(t *testing.T) {
	t.Parallel()

	image := repo + "@" + digest

	for _, name := range []string{"bash", "dash", "ash"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			shell, err := exec.LookPath(name)
			if err != nil {
				t.Skipf("%s is not installed", name)
			}

			checkRanIn(t, shell, image)
			checkRanIn(t, shell, digest, pull, never)
			checkRefused(t, shell, repo+":latest")

			for _, args := range refusedArgs() {
				checkRefused(t, shell, append([]string{image}, args...)...)
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

func composeText(t *testing.T) string {
	t.Helper()

	raw, err := os.ReadFile("compose.yaml")
	if err != nil {
		t.Fatal(err)
	}

	return string(raw)
}

// TestComposeTakesTheImageVariable keeps the Compose file and the script
// agreed on the variable name.
func TestComposeTakesTheImageVariable(t *testing.T) {
	t.Parallel()

	raw := composeText(t)

	if !strings.Contains(raw, "${GITHUB_METRICS_IMAGE:?") {
		t.Error("compose.yaml does not take its image from GITHUB_METRICS_IMAGE")
	}
}

func tunnelService(t *testing.T) string {
	t.Helper()

	raw := composeText(t)

	var block []string

	inside := false
	top := ""

	for line := range strings.SplitSeq(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			if inside {
				block = append(block, line)
			}

			continue
		}

		if !strings.HasPrefix(line, " ") {
			top = line
			inside = false

			continue
		}

		isKey := !strings.HasPrefix(line, "   ")

		switch {
		case top == "services:" && line == "  tunnel:":
			inside = true
		case isKey:
			inside = false
		}

		if inside {
			block = append(block, line)
		}
	}

	if len(block) == 0 {
		t.Fatal("compose.yaml has no tunnel service")
	}

	return strings.Join(block, "\n")
}

// TestTunnelServiceSettings reads the block for the pinned image, the token
// secret, the hardening keys and the absence of any port.
func TestTunnelServiceSettings(t *testing.T) {
	t.Parallel()

	block := tunnelService(t)

	image := regexp.MustCompile(`(?m)^    image: cloudflare/cloudflared:[0-9][0-9.]*@sha256:[0-9a-f]{64}$`)
	if !image.MatchString(block) {
		t.Error("the tunnel image is not cloudflare/cloudflared:<tag>@sha256:<64 hex digits>")
	}

	for _, want := range []string{
		"\n    user: \"65532:65532\"\n",
		"\n    read_only: true\n",
		"\n    cap_drop: [ALL]\n",
		"\n    security_opt:\n      - no-new-privileges:true\n",
		"\n    secrets:\n      - tunnel_token\n",
		"\n      - --token-file\n      - /run/secrets/tunnel_token\n",
		"\n    depends_on:\n      receiver:\n        condition: service_healthy\n",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("the tunnel service lacks %q", strings.TrimSpace(want))
		}
	}

	if !strings.Contains(composeText(t), "\n  tunnel_token:\n    file: ./secrets/tunnel_token\n") {
		t.Error("compose.yaml does not define the tunnel_token secret as ./secrets/tunnel_token")
	}

	for _, banned := range []string{"privileged", "cap_add"} {
		if strings.Contains(block, banned) {
			t.Errorf("the tunnel service contains %q", banned)
		}
	}

	for _, banned := range []string{"ports:", "network_mode", "TUNNEL_TOKEN"} {
		if strings.Contains(composeText(t), banned) {
			t.Errorf("compose.yaml contains %q", banned)
		}
	}
}
