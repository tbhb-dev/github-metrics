// SPDX-License-Identifier: Apache-2.0
// Copyright Tony Burns

package deploy_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
)

const (
	digest        = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	repo          = "ghcr.io/tbhb-dev/github-metrics"
	pull          = "--pull"
	never         = "never"
	envFile       = "--env-file"
	forceRecreate = "--force-recreate"
)

// result is what one run of up.sh left behind.
type result struct {
	output   string
	record   string
	helper   string
	dir      string
	imageEnv string
	code     int
	ran      bool
}

// runUp runs up.sh under /bin/sh.
func runUp(t *testing.T, args ...string) result {
	t.Helper()

	return runIn(t, "/bin/sh", args...)
}

// runIn runs up.sh with a private fixture and a Docker stub. Each source starts
// with mode 600. PATH lists only the stub directory and links to host tools.
// A helper call records its arguments before the stub returns. Successful
// Compose calls also record the image from its environment file. Tests inspect
// those records without a daemon or actual key material.
func runIn(t *testing.T, shell string, args ...string) result {
	t.Helper()

	return runConfigured(t, shell, nil, args...)
}

func writeFixture(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()

	err := os.WriteFile(path, []byte(content), mode)
	if err != nil {
		t.Fatal(err)
	}
}

func makeSecrets(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()

	err := os.Mkdir(filepath.Join(dir, "secrets"), 0o700)
	if err != nil {
		t.Fatal(err)
	}

	return dir
}

func readFixture(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}

func fixtureMode(t *testing.T, path string) os.FileMode {
	t.Helper()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	return info.Mode().Perm()
}

func prepareUpFixture(t *testing.T) string {
	t.Helper()

	dir := makeSecrets(t)
	writeFixture(t, filepath.Join(dir, "up.sh"), readFixture(t, "up.sh"), 0o600)
	writeFixture(t, filepath.Join(dir, "secrets/github_app_key"), "test key contents\n", 0o600)

	stub := `#!/bin/sh
if [ "$1" = run ]; then
	printf '%s\0' "$@" > helper-record
	if [ -f helper-exit ]; then read -r code < helper-exit; exit "$code"; fi
	exit 0
fi
for arg do
	case "$arg" in */secrets/.image.*) IFS= read -r image_line < "$arg" ;; esac
done
printf '%s\0' "${GITHUB_METRICS_IMAGE:-}" "$image_line" "$@" > record
`
	writeFixture(t, filepath.Join(dir, "docker"), stub, 0o700)
	writeFixture(t, filepath.Join(dir, "sudo"), "#!/bin/sh\nunset GITHUB_METRICS_IMAGE\nexec \"$@\"\n", 0o700)

	// PATH limits the host tools the script can call.
	for _, tool := range []string{"grep", "dirname", "mktemp", "rm"} {
		path, lookErr := exec.LookPath(tool)
		if lookErr != nil {
			t.Fatal(lookErr)
		}

		linkErr := os.Symlink(path, filepath.Join(dir, tool))
		if linkErr != nil {
			t.Fatal(linkErr)
		}
	}

	return dir
}

func runConfigured(t *testing.T, shell string, setup func(string), args ...string) result {
	t.Helper()

	dir := prepareUpFixture(t)
	if setup != nil {
		setup(dir)
	}

	argv := append([]string{"./up.sh"}, args...)
	cmd := exec.CommandContext(t.Context(), shell, argv...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + dir, "LC_ALL=en_US.UTF-8"}

	_, statErr := os.Stat(filepath.Join(dir, "use-sudo"))
	if statErr == nil {
		cmd.Env = append(cmd.Env, "DEPLOY_DOCKER_SUDO=1")
	}

	out, err := cmd.CombinedOutput()
	res := result{output: string(out), record: "", helper: "", dir: dir, imageEnv: "", code: 0, ran: false}

	var exitErr *exec.ExitError

	switch {
	case errors.As(err, &exitErr):
		res.code = exitErr.ExitCode()
	case err != nil:
		t.Fatal(err)
	}

	got, readErr := os.ReadFile(filepath.Join(dir, "record"))
	if readErr == nil {
		res.ran = true
		res.record = string(got)

		for arg := range strings.SplitSeq(res.record, "\x00") {
			if strings.HasPrefix(arg, filepath.Join(dir, "secrets/.image.")) {
				res.imageEnv = arg
			}
		}

		_, imageErr := os.Stat(res.imageEnv)
		if !errors.Is(imageErr, os.ErrNotExist) {
			t.Fatalf("image environment file remains after exit: %v", imageErr)
		}
	}

	helper, helperErr := os.ReadFile(filepath.Join(dir, "helper-record"))
	if helperErr == nil {
		res.helper = string(helper)
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

	want := append(
		[]string{
			image,
			"GITHUB_METRICS_IMAGE=" + image,
			"compose",
			envFile,
			res.imageEnv,
			"-f",
			filepath.Join(res.dir, "compose.yaml"),
			"up",
			"-d",
			forceRecreate,
		},
		args...,
	)

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
		"env file":           {envFile, "other.env"},
		"pull always":        {pull, "always"},
		"pull missing value": {pull},
		"pull joined":        {"--pull=never"},
		"pull in one word":   {"--pull never"},
		"extra after pull":   {pull, never, forceRecreate},
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

func serviceBlock(t *testing.T, service string) string {
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
		case top == "services:" && line == "  "+service+":":
			inside = true
		case isKey:
			inside = false
		}

		if inside {
			block = append(block, line)
		}
	}

	if len(block) == 0 {
		t.Fatalf("compose.yaml has no %s service", service)
	}

	return strings.Join(block, "\n")
}

func TestReceiverServiceSettings(t *testing.T) {
	t.Parallel()

	block := serviceBlock(t, "receiver")

	if strings.Contains(block, "\n    runtime:") {
		t.Error("the receiver service overrides the default runtime")
	}

	for _, want := range []string{
		"\n    user: \"65532:65532\"\n",
		"\n    read_only: true\n",
		"\n      OTEL_EXPORTER_OTLP_ENDPOINT: ${OTEL_EXPORTER_OTLP_ENDPOINT:-}\n",
		"\n      GITHUB_APP_KEY_FILE: /run/secrets/github_app_key\n",
		"\n    secrets:\n      - webhook_secret\n      - github_app_key\n",
		"\n      STORE_FILE: /var/lib/github-metrics/store.db\n",
		"\n    volumes:\n      - store:/var/lib/github-metrics\n",
		"\n    networks:\n      - edge\n      - metrics\n",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("the receiver service lacks %q", strings.TrimSpace(want))
		}
	}

	if !strings.Contains(composeText(t), "\nvolumes:\n  store: {}\n") {
		t.Error("compose.yaml does not define the named store volume")
	}

	const network = "\n  metrics:\n" +
		"    name: \"${METRICS_NETWORK:?set METRICS_NETWORK to the existing collector network}\"\n" +
		"    external: true\n"

	if !strings.Contains(composeText(t), network) {
		t.Error("compose.yaml does not define the external metrics network with its required METRICS_NETWORK name")
	}
}

// TestTunnelServiceSettings reads the block for the pinned image, the token
// secret, the hardening keys and the absence of any port.
func TestTunnelServiceSettings(t *testing.T) {
	t.Parallel()

	block := serviceBlock(t, "tunnel")

	image := regexp.MustCompile(`(?m)^    image: cloudflare/cloudflared:[0-9][0-9.]*@sha256:[0-9a-f]{64}$`)
	if !image.MatchString(block) {
		t.Error("the tunnel image is not cloudflare/cloudflared:<tag>@sha256:<64 hex digits>")
	}

	for _, want := range []string{
		"\n    runtime: runc\n",
		"\n    user: \"65532:65532\"\n",
		"\n    read_only: true\n",
		"\n    cap_drop: [ALL]\n",
		"\n    security_opt:\n      - no-new-privileges:true\n",
		"\n    secrets:\n      - tunnel_token\n",
		"\n      - --token-file\n      - /run/secrets/tunnel_token\n",
		"\n    depends_on:\n      receiver:\n        condition: service_healthy\n",
		"\n    networks:\n      - edge\n    healthcheck:\n",
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

func badKeySource(t *testing.T, dir, kind string) {
	t.Helper()

	key := filepath.Join(dir, "secrets/github_app_key")

	err := os.Remove(key)
	if err != nil {
		t.Fatal(err)
	}

	switch kind {
	case "directory":
		err = os.Mkdir(key, 0o700)
	case "symlink":
		err = os.Symlink("../up.sh", key)
	}

	if err != nil {
		t.Fatal(err)
	}
}

func TestUpRequiresKeySource(t *testing.T) {
	t.Parallel()

	for _, kind := range []string{"missing", "directory", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()

			res := runConfigured(t, "/bin/sh", func(dir string) {
				badKeySource(t, dir, kind)
			}, digest)
			if res.code != 2 || res.ran || res.helper != "" ||
				!strings.Contains(res.output, "must exist as a regular file") {
				t.Fatalf("source %s: exit %d, ran=%v: %s", kind, res.code, res.ran, res.output)
			}
		})
	}
}

func TestUpStopsAfterHelperFailure(t *testing.T) {
	t.Parallel()

	res := runConfigured(t, "/bin/sh", func(dir string) {
		writeFixture(t, filepath.Join(dir, "helper-exit"), "1\n", 0o600)
	}, digest)

	if res.code == 0 || res.ran || res.helper == "" {
		t.Fatalf("helper failure: exit %d, ran=%v: %s", res.code, res.ran, res.output)
	}
}

func TestUpWithSudoAndDotEnv(t *testing.T) {
	t.Parallel()

	res := runConfigured(t, "/bin/sh", func(dir string) {
		writeFixture(t, filepath.Join(dir, "use-sudo"), "", 0o600)
		writeFixture(t, filepath.Join(dir, ".env"), "METRICS_NETWORK=metrics\n", 0o600)
	}, digest)

	want := []string{
		"",
		"GITHUB_METRICS_IMAGE=" + digest,
		"compose",
		envFile,
		filepath.Join(res.dir, ".env"),
		envFile,
		res.imageEnv,
		"-f",
		filepath.Join(res.dir, "compose.yaml"),
		"up",
		"-d",
		forceRecreate,
	}

	got := strings.Split(strings.TrimSuffix(res.record, "\x00"), "\x00")
	if res.code != 0 || !res.ran || !slices.Equal(got, want) {
		t.Fatalf("sudo deployment: exit %d, got %q, want %q: %s", res.code, got, want, res.output)
	}
}

func keyHelperScript(t *testing.T) string {
	t.Helper()

	res := runUp(t, digest)
	if res.code != 0 || res.helper == "" {
		t.Fatalf("no helper: %s", res.output)
	}

	args := strings.Split(strings.TrimSuffix(res.helper, "\x00"), "\x00")
	for _, want := range []string{
		"--rm",
		"--read-only",
		"--network\x00none",
		"--log-driver\x00none",
		"--user\x000:0",
		"--cap-add\x00CHOWN",
		"--cap-add\x00DAC_OVERRIDE",
		"busybox:1.37.0@sha256:a7dbc319b1438115a207e11930dc4bb3e643dcb3ec0bdd694bb36cd8fad289ec",
	} {
		if !strings.Contains(res.helper, want) {
			t.Errorf("helper lacks %q", want)
		}
	}

	return args[len(args)-1]
}

// Host tools run the helper body in tests. Ownership changes use a stub.
// Before copying, another stub checks directory permissions.
func keyHelperCommand(t *testing.T, script, dir string) *exec.Cmd {
	t.Helper()

	prefix := `
chown() { printf '%s\n' "$*" >> "$KEY_TEST_DIR/owners"; }
cp() {
	[ ! -f "$KEY_TEST_DIR/fail-copy" ] || return 1
	[ "$(stat -c %a "$KEY_TEST_DIR/secrets/runtime")" = 700 ] || exit 1
	command cp "$@"
}
mv() { command mv -f "$2" "$3"; }
`
	if runtime.GOOS == "darwin" {
		prefix = "stat() { command stat -f %Lp \"$3\"; }\n" + prefix
	}

	script = strings.ReplaceAll(script, "/secrets", filepath.Join(dir, "secrets"))
	script = strings.ReplaceAll(script, "/source", filepath.Join(dir, "secrets/github_app_key"))
	//nolint:gosec // the script comes from the tracked deploy file
	cmd := exec.CommandContext(
		t.Context(),
		"/bin/sh",
		"-ec",
		prefix+script,
	)

	cmd.Env = append(os.Environ(), "KEY_TEST_DIR="+dir)

	return cmd
}

func checkKeyCopy(t *testing.T, dir string, mode os.FileMode) {
	t.Helper()

	copyPath := filepath.Join(dir, "secrets/runtime/github_app_key")
	if fixtureMode(t, copyPath) != 0o400 {
		t.Error("runtime key must have mode 400")
	}

	owners := readFixture(t, filepath.Join(dir, "owners"))
	if !strings.Contains(owners, "0:0 "+filepath.Join(dir, "secrets/runtime")) ||
		!strings.Contains(owners, "65532:65532 "+copyPath+".") {
		t.Errorf("wrong owner requests: %s", owners)
	}

	if fixtureMode(t, filepath.Join(dir, "secrets/github_app_key")) != mode {
		t.Error("source mode changed")
	}
}

func checkRejectedMode(t *testing.T, dir string, out []byte, err error) {
	t.Helper()

	if err == nil || !strings.Contains(string(out), "must have mode 400 or 600") {
		t.Fatalf("invalid mode: %v: %s", err, out)
	}

	_, statErr := os.Stat(filepath.Join(dir, "secrets/runtime"))
	if !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("invalid source created runtime directory: %v", statErr)
	}
}

func TestKeyHelperModes(t *testing.T) {
	t.Parallel()

	script := keyHelperScript(t)

	for _, mode := range []os.FileMode{0o400, 0o600, 0o000, 0o200, 0o640, 0o644, 0o660, 0o700} {
		t.Run(mode.String(), func(t *testing.T) {
			t.Parallel()

			dir := makeSecrets(t)
			writeFixture(t, filepath.Join(dir, "secrets/github_app_key"), "test key contents\n", mode)
			out, err := keyHelperCommand(t, script, dir).CombinedOutput()

			allowed := mode == 0o400 || mode == 0o600
			if !allowed {
				checkRejectedMode(t, dir, out, err)

				return
			}

			if err != nil {
				t.Fatalf("mode %o: %v: %s", mode, err, out)
			}

			checkKeyCopy(t, dir, mode)
		})
	}
}

func TestKeyHelperRotationAndFailure(t *testing.T) {
	t.Parallel()

	script := keyHelperScript(t)
	dir := makeSecrets(t)
	key := filepath.Join(dir, "secrets/github_app_key")
	copyPath := filepath.Join(dir, "secrets/runtime/github_app_key")

	for _, content := range []string{"first test key", "second test key"} {
		writeFixture(t, key, content, 0o600)

		out, err := keyHelperCommand(t, script, dir).CombinedOutput()
		if err != nil {
			t.Fatalf("prepare: %v: %s", err, out)
		}

		if readFixture(t, copyPath) != content {
			t.Error("runtime copy doesn't match source")
		}
	}

	writeFixture(t, filepath.Join(dir, "fail-copy"), "", 0o600)
	writeFixture(t, key, "third test key", 0o600)

	out, err := keyHelperCommand(t, script, dir).CombinedOutput()
	if err == nil {
		t.Fatalf("copy failure succeeded: %s", out)
	}

	if readFixture(t, copyPath) != "second test key" {
		t.Error("failed copy replaced the previous key")
	}

	entries, readErr := os.ReadDir(filepath.Join(dir, "secrets/runtime"))
	if readErr != nil {
		t.Fatal(readErr)
	}

	if len(entries) != 1 || entries[0].Name() != "github_app_key" {
		t.Errorf("temporary files remain: %v", entries)
	}
}

func TestComposeKeySecret(t *testing.T) {
	t.Parallel()

	if !strings.Contains(composeText(t), "\n  github_app_key:\n    file: ./secrets/runtime/github_app_key\n") {
		t.Error("App key secret doesn't use the protected runtime copy")
	}

	if strings.Contains(serviceBlock(t, "tunnel"), "github_app_key") {
		t.Error("tunnel mounts the App key")
	}
}
