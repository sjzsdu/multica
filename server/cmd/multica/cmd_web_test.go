package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveWebListenAddrPicksPort(t *testing.T) {
	addr, err := resolveWebListenAddr("127.0.0.1:0")
	if err != nil {
		t.Fatalf("resolveWebListenAddr: %v", err)
	}
	if strings.HasSuffix(addr, ":0") {
		t.Fatalf("expected selected port, got %q", addr)
	}
	if !strings.HasPrefix(addr, "127.0.0.1:") {
		t.Fatalf("expected loopback addr, got %q", addr)
	}
}

func TestResolveWebListenAddrRejectsInvalid(t *testing.T) {
	_, err := resolveWebListenAddr("badaddr")
	if err == nil || !strings.Contains(err.Error(), "invalid --addr") {
		t.Fatalf("expected invalid --addr error, got %v", err)
	}
}

func TestNormalizeWebAPIURL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "origin", raw: "https://api.example.com/", want: "https://api.example.com"},
		{name: "strip api suffix", raw: "https://api.example.com/api", want: "https://api.example.com"},
		{name: "keep prefix", raw: "https://example.com/multica/api", want: "https://example.com/multica"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeWebAPIURL(tt.raw)
			if err != nil {
				t.Fatalf("normalizeWebAPIURL: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizeWebAPIURLRejectsInvalid(t *testing.T) {
	_, err := normalizeWebAPIURL("ftp://example.com")
	if err == nil || !strings.Contains(err.Error(), "invalid --api-url") {
		t.Fatalf("expected invalid --api-url error, got %v", err)
	}
}

func TestParseDotEnvFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("# comment\nPORT=19090\nexport POSTGRES_DB=multica_test\nQUOTED=\"hello world\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	vars, err := parseDotEnvFile(path)
	if err != nil {
		t.Fatalf("parseDotEnvFile: %v", err)
	}
	if vars["PORT"] != "19090" || vars["POSTGRES_DB"] != "multica_test" || vars["QUOTED"] != "hello world" {
		t.Fatalf("unexpected vars: %#v", vars)
	}
}

func TestDiscoverLocalWebProject(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("PORT=18080\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "server", "cmd", "server"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "server", "cmd", "server", "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "scripts", "ensure-postgres.sh"), []byte("#!/usr/bin/env bash\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "apps", "web")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	got, ok := discoverLocalWebProject(nested)
	if !ok || got != root {
		t.Fatalf("got %q, %v; want %q, true", got, ok, root)
	}
}

func TestEmbeddedWebEnvSetsRuntimeProxy(t *testing.T) {
	env := embeddedWebEnv([]string{"PORT=old", "REMOTE_API_URL=http://old"}, "127.0.0.1:4321", "https://api.example.com", []string{"/deps/a", "/deps/b"})
	joined := "\n" + strings.Join(env, "\n") + "\n"
	for _, want := range []string{
		"\nHOSTNAME=127.0.0.1\n",
		"\nPORT=4321\n",
		"\nNODE_ENV=production\n",
		"\nREMOTE_API_URL=https://api.example.com\n",
		"\nNEXT_PUBLIC_API_URL=\n",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in env %q", want, joined)
		}
	}
	if !strings.Contains(joined, "/deps/a") || !strings.Contains(joined, "/deps/b") {
		t.Fatalf("NODE_PATH did not include dependency dirs: %q", joined)
	}
}

func TestWebURLUsesLoopbackForWildcard(t *testing.T) {
	if got := webURL("0.0.0.0:3000"); got != "http://127.0.0.1:3000" {
		t.Fatalf("got %q", got)
	}
}

func TestSplitShellLike(t *testing.T) {
	got, err := splitShellLike(`make server ARG="hello world"`)
	if err != nil {
		t.Fatalf("splitShellLike: %v", err)
	}
	want := []string{"make", "server", "ARG=hello world"}
	if len(got) != len(want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	}
}
