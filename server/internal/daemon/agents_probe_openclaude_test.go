package daemon

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestProbeAgentCLIsDiscoversOpenClaudeFromDescriptor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}

	fakeDir := t.TempDir()
	writeDaemonTestExecutable(t, filepath.Join(fakeDir, "openclaude"), []byte("#!/bin/sh\nexit 0\n"))

	orig := resolveAgentsViaLoginShell
	t.Cleanup(func() { resolveAgentsViaLoginShell = orig })
	resolveAgentsViaLoginShell = func([]string) map[string]string { return map[string]string{} }
	resetShellResolveCacheForTest(t)

	t.Setenv("PATH", fakeDir)
	t.Setenv("MULTICA_OPENCLAUDE_PATH", "")
	t.Setenv("MULTICA_OPENCLAUDE_MODEL", "local-model")

	entry, ok := probeAgentCLIs()["openclaude"]
	if !ok {
		t.Fatal("openclaude was not discovered from the built-in runtime descriptor")
	}
	if entry.Command != "openclaude" {
		t.Fatalf("openclaude command = %q, want openclaude", entry.Command)
	}
	if entry.Model != "local-model" {
		t.Fatalf("openclaude model = %q, want local-model", entry.Model)
	}
}
