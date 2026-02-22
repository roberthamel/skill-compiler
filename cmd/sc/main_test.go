package main

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// newRootCmd builds the same command tree as main() for testing.
func newRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:     "sc",
		Short:   "Skill Compiler — compile interface specs into Agent Skills",
		Version: version,
	}
	rootCmd.AddCommand(
		newGenerateCmd(),
		newInitCmd(),
		newValidateCmd(),
		newDiffCmd(),
		newServeCmd(),
		newConfigCmd(),
	)
	return rootCmd
}

// execCmd runs a cobra command with the given args and captures stdout/stderr.
func execCmd(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := newRootCmd()
	cmd.SetArgs(args)

	var outBuf, errBuf bytes.Buffer
	cmd.SetOut(&outBuf)
	cmd.SetErr(&errBuf)

	// Redirect os.Stdout/os.Stderr so fmt.Print* calls are captured
	oldStdout := os.Stdout
	oldStderr := os.Stderr
	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	os.Stdout = wOut
	os.Stderr = wErr

	err = cmd.Execute()

	_ = wOut.Close()
	_ = wErr.Close()
	os.Stdout = oldStdout
	os.Stderr = oldStderr

	var pipeOut, pipeErr bytes.Buffer
	_, _ = pipeOut.ReadFrom(rOut)
	_, _ = pipeErr.ReadFrom(rErr)

	stdout = outBuf.String() + pipeOut.String()
	stderr = errBuf.String() + pipeErr.String()
	return
}

// validInstructionsFixture is a minimal COMPILER_INSTRUCTIONS.md pointing to
// the petstore.yaml fixture in the openapi plugin testdata.
func validInstructionsFixture(t *testing.T, dir string, specPath string) string {
	t.Helper()
	content := fmt.Sprintf(`---
name: test-tool
spec: %s
out: ./output/
---

# Product

test-tool is a sample tool for testing.

# Workflows

## Basic workflow
1. Step one
2. Step two

# Examples

Example of usage here.

# Common patterns

Pattern one.
`, specPath)
	path := filepath.Join(dir, "COMPILER_INSTRUCTIONS.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing instructions fixture: %v", err)
	}
	return path
}

func TestGenerateDryRun(t *testing.T) {
	dir := t.TempDir()

	// Copy petstore.yaml to temp dir
	petstore, err := os.ReadFile("../../internal/plugins/openapi/testdata/petstore.yaml")
	if err != nil {
		t.Fatalf("reading petstore fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "petstore.yaml"), petstore, 0o644); err != nil {
		t.Fatalf("writing petstore.yaml: %v", err)
	}

	validInstructionsFixture(t, dir, "./petstore.yaml")

	// Set HOME to isolate config
	t.Setenv("HOME", dir)

	// Change to temp dir so the command finds COMPILER_INSTRUCTIONS.md
	orig, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	stdout, stderr, err := execCmd(t, "generate", "--dry-run")
	if err != nil {
		t.Fatalf("generate --dry-run failed: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, "Dry run complete") {
		t.Errorf("stdout should contain 'Dry run complete', got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "Parsing spec sources") {
		t.Errorf("stdout should contain 'Parsing spec sources', got:\n%s", stdout)
	}
}

func TestGenerateErrorNoInstructions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	orig, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	_, _, err := execCmd(t, "generate")
	if err == nil {
		t.Fatal("expected error when no instructions file exists")
	}
	if !strings.Contains(err.Error(), "sc init") {
		t.Errorf("error should suggest 'sc init', got: %v", err)
	}
}

func TestInitRequiresName(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	orig, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	_, _, err := execCmd(t, "init")
	if err == nil {
		t.Fatal("expected error without --name flag")
	}
	if !strings.Contains(err.Error(), "--name") {
		t.Errorf("error should mention --name, got: %v", err)
	}
}

func TestInitRefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	// Create existing instructions file
	if err := os.WriteFile(filepath.Join(dir, "COMPILER_INSTRUCTIONS.md"), []byte("existing"), 0o644); err != nil {
		t.Fatalf("writing existing file: %v", err)
	}

	orig, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	_, _, err := execCmd(t, "init", "--name", "test-tool")
	if err == nil {
		t.Fatal("expected error when instructions file already exists")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error should mention 'already exists', got: %v", err)
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error should suggest --force, got: %v", err)
	}
}

func TestValidateWarningsMissingProduct(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	// Create instructions file without Product section
	content := `---
name: test-tool
spec: ./petstore.yaml
out: ./output/
---

# Workflows

Some workflow.
`
	if err := os.WriteFile(filepath.Join(dir, "COMPILER_INSTRUCTIONS.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("writing instructions: %v", err)
	}

	// Copy petstore.yaml
	petstore, err := os.ReadFile("../../internal/plugins/openapi/testdata/petstore.yaml")
	if err != nil {
		t.Fatalf("reading petstore fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "petstore.yaml"), petstore, 0o644); err != nil {
		t.Fatalf("writing petstore.yaml: %v", err)
	}

	orig, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	_, stderr, _ := execCmd(t, "validate")
	// The validate command writes warnings to stderr
	if !strings.Contains(stderr, "Product") {
		t.Errorf("stderr should warn about missing Product section, got:\n%s", stderr)
	}
}

func TestDiffErrorNoInstructions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	orig, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	// Without COMPILER_INSTRUCTIONS.md, diff should error
	_, _, err := execCmd(t, "diff")
	if err == nil {
		t.Fatal("expected error when no instructions file exists")
	}
}

// scEnv returns an environment for running the sc binary in a temp directory.
// It sets HOME to the temp dir (for config isolation) but preserves GOPATH and
// GOMODCACHE so Go toolchain commands don't pollute the temp dir with read-only
// module cache files.
func scEnv(t *testing.T, dir string) []string {
	t.Helper()
	env := os.Environ()
	env = append(env, "HOME="+dir)
	return env
}

// buildSC builds the sc binary into the given directory and returns the path.
// It does NOT set HOME to the temp dir for the build command, avoiding module
// cache pollution.
func buildSC(t *testing.T, dir string) string {
	t.Helper()
	repoRoot := filepath.Join(mustGetwd(t), "..", "..")
	binPath := filepath.Join(dir, "sc")
	build := exec.Command("go", "build", "-o", binPath, "./cmd/sc")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building sc: %v\n%s", err, out)
	}
	return binPath
}

func TestDiffDriftDetection(t *testing.T) {
	// We test the drift detection logic via a subprocess since runDiff calls
	// os.Exit(1) directly on drift, which can't be captured in-process.
	dir := t.TempDir()

	// Copy petstore.yaml
	petstore, err := os.ReadFile("../../internal/plugins/openapi/testdata/petstore.yaml")
	if err != nil {
		t.Fatalf("reading petstore fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "petstore.yaml"), petstore, 0o644); err != nil {
		t.Fatalf("writing petstore.yaml: %v", err)
	}

	validInstructionsFixture(t, dir, "./petstore.yaml")
	binPath := buildSC(t, dir)

	// Run diff from the temp directory — should exit 1 (drift, no lockfile)
	cmd := exec.Command(binPath, "diff")
	cmd.Dir = dir
	cmd.Env = scEnv(t, dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected non-zero exit from diff with no lockfile")
	}
	if !strings.Contains(string(out), "DRIFTED") && !strings.Contains(string(out), "changed") {
		t.Logf("diff output: %s", string(out))
	}
}

func TestDiffUpToDate(t *testing.T) {
	// Test that diff exits 0 when lockfile matches current inputs.
	dir := t.TempDir()

	// Copy petstore.yaml
	petstore, err := os.ReadFile("../../internal/plugins/openapi/testdata/petstore.yaml")
	if err != nil {
		t.Fatalf("reading petstore fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "petstore.yaml"), petstore, 0o644); err != nil {
		t.Fatalf("writing petstore.yaml: %v", err)
	}

	validInstructionsFixture(t, dir, "./petstore.yaml")
	binPath := buildSC(t, dir)
	repoRoot := filepath.Join(mustGetwd(t), "..", "..")

	// Run the write-lockfile helper to create a lockfile with correct hashes.
	// Do NOT set HOME for go run — it would pollute the temp dir with module cache.
	helper := exec.Command("go", "run", "./cmd/sc/testdata/write_lockfile.go", dir)
	helper.Dir = repoRoot
	if out, err := helper.CombinedOutput(); err != nil {
		t.Fatalf("running lockfile helper: %v\n%s", err, out)
	}

	// Verify lockfile was created
	if _, err := os.Stat(filepath.Join(dir, ".sc-lock.json")); err != nil {
		t.Fatalf("lockfile not created: %v", err)
	}

	// Run diff — should exit 0 (all up to date)
	cmd := exec.Command(binPath, "diff")
	cmd.Dir = dir
	cmd.Env = scEnv(t, dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("expected exit 0 from diff with matching lockfile, got error: %v\noutput: %s", err, out)
	}
	if !strings.Contains(string(out), "up to date") {
		t.Errorf("output should contain 'up to date', got: %s", string(out))
	}
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return wd
}

func TestConfigSetListReset(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	// Set a config value
	_, _, err := execCmd(t, "config", "set", "provider", "openai")
	if err != nil {
		t.Fatalf("config set failed: %v", err)
	}

	// List and verify
	stdout, _, err := execCmd(t, "config", "list")
	if err != nil {
		t.Fatalf("config list failed: %v", err)
	}
	if !strings.Contains(stdout, "openai") {
		t.Errorf("config list should show 'openai', got:\n%s", stdout)
	}

	// Reset
	_, _, err = execCmd(t, "config", "reset")
	if err != nil {
		t.Fatalf("config reset failed: %v", err)
	}

	// Verify reset
	stdout, _, err = execCmd(t, "config", "list")
	if err != nil {
		t.Fatalf("config list after reset failed: %v", err)
	}
	if strings.Contains(stdout, "openai") {
		t.Errorf("config list after reset should not show 'openai', got:\n%s", stdout)
	}
}

func TestServeRespondsToHTTP(t *testing.T) {
	dir := t.TempDir()

	// Create a file to serve
	if err := os.WriteFile(filepath.Join(dir, "llms.txt"), []byte("# test-tool llms.txt\n"), 0o644); err != nil {
		t.Fatalf("writing llms.txt: %v", err)
	}

	// Find a free port
	listener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("finding free port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()

	// Start serve in a goroutine
	cmd := newRootCmd()
	cmd.SetArgs([]string{"serve", "--dir", dir, "--port", fmt.Sprintf("%d", port)})

	errCh := make(chan error, 1)
	go func() {
		errCh <- cmd.Execute()
	}()

	// Wait for server to start
	addr := fmt.Sprintf("http://localhost:%d", port)
	var resp *http.Response
	for i := 0; i < 50; i++ {
		time.Sleep(50 * time.Millisecond)
		resp, err = http.Get(addr + "/llms.txt")
		if err == nil {
			break
		}
	}
	if err != nil {
		t.Fatalf("server did not start: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /llms.txt status = %d, want 200", resp.StatusCode)
	}

	var body bytes.Buffer
	_, _ = body.ReadFrom(resp.Body)
	if !strings.Contains(body.String(), "test-tool") {
		t.Errorf("GET /llms.txt body = %q, want to contain 'test-tool'", body.String())
	}
}
