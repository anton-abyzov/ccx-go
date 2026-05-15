package main

import (
	"os/exec"
	"strings"
	"testing"
)

func runCLI(t *testing.T, args ...string) string {
	t.Helper()

	cmdArgs := append([]string{"run", "."}, args...)
	cmd := exec.Command("go", cmdArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go %s failed: %v\n%s", strings.Join(cmdArgs, " "), err, out)
	}

	return string(out)
}

func TestHelpUsesCCXGoCommandName(t *testing.T) {
	out := runCLI(t, "--help")

	if !strings.Contains(out, "Usage:\n  ccx-go [prompt]") {
		t.Fatalf("help output should use ccx-go in usage, got:\n%s", out)
	}
	if strings.Contains(out, "Usage:\n  claude [prompt]") {
		t.Fatalf("help output should not use stale claude command name, got:\n%s", out)
	}
}

func TestVersionUsesCCXGoCommandName(t *testing.T) {
	out := strings.TrimSpace(runCLI(t, "--version"))

	if out != "ccx-go version dev" {
		t.Fatalf("version output = %q, want %q", out, "ccx-go version dev")
	}
}
