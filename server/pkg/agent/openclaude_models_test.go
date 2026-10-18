package agent

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverOpenClaudeModelsFromOpenAICompatibleCatalog(t *testing.T) {
	serverURL := "https://models.example.test/v1"
	previousClient := openClaudeHTTPClient
	openClaudeHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != serverURL+"/models" {
			t.Fatalf("request URL = %q, want %s/models", r.URL.String(), serverURL)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("authorization = %q, want test-key", r.Header.Get("Authorization"))
		}
		return openClaudeTestResponse(`{"data":[{"id":"model-a"},{"id":"model-b"},{"id":"model-a"}]}`), nil
	})}
	t.Cleanup(func() { openClaudeHTTPClient = previousClient })

	t.Setenv("OPENAI_BASE_URL", serverURL)
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("MULTICA_OPENCLAUDE_MODEL", "model-b")
	command := Command{Path: writeOpenClaudeTestExecutable(t)}

	models, err := discoverOpenClaudeModels(context.Background(), command)
	if err != nil {
		t.Fatalf("discoverOpenClaudeModels: %v", err)
	}
	if got, want := len(models), 2; got != want {
		t.Fatalf("model count = %d, want %d", got, want)
	}
	if models[1].ID != "model-b" || !models[1].Default {
		t.Fatalf("configured model = %#v, want model-b as default", models[1])
	}
}

func TestDiscoverOpenClaudeModelsFromOllamaCatalog(t *testing.T) {
	serverURL := "https://ollama.example.test/v1"
	previousClient := openClaudeHTTPClient
	openClaudeHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://ollama.example.test/api/tags" {
			t.Fatalf("request URL = %q, want https://ollama.example.test/api/tags", r.URL.String())
		}
		return openClaudeTestResponse(`{"models":[{"name":"qwen3-coder"},{"name":"llama3"}]}`), nil
	})}
	t.Cleanup(func() { openClaudeHTTPClient = previousClient })

	t.Setenv("OPENAI_BASE_URL", serverURL)
	t.Setenv("MULTICA_OPENCLAUDE_MODEL", "qwen3-coder")
	// The profile name is what selects the Ollama /api/tags endpoint. Use a
	// temporary profile so the test does not depend on the developer's home.
	profileDir := t.TempDir()
	profilePath := filepath.Join(profileDir, ".openclaude-profile.json")
	if err := os.WriteFile(profilePath, []byte(`{"profile":"ollama","env":{}}`), 0o600); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	t.Setenv("OPENCLAUDE_CONFIG_DIR", profileDir)

	models, err := discoverOpenClaudeModels(context.Background(), Command{Path: writeOpenClaudeTestExecutable(t)})
	if err != nil {
		t.Fatalf("discoverOpenClaudeModels: %v", err)
	}
	if len(models) != 2 || models[0].ID != "qwen3-coder" || models[1].ID != "llama3" {
		t.Fatalf("models = %#v, want Ollama catalog", models)
	}
}

func TestOpenClaudeConfiguredModelsUseProfileWhenEnvironmentIsEmpty(t *testing.T) {
	profileDir := t.TempDir()
	profilePath := filepath.Join(profileDir, ".openclaude-profile.json")
	if err := os.WriteFile(profilePath, []byte(`{"profile":"openai","env":{"OPENAI_MODEL":"profile-model"}}`), 0o600); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	t.Setenv("OPENCLAUDE_CONFIG_DIR", profileDir)
	t.Setenv("OPENAI_BASE_URL", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENAI_MODEL", "")

	models, err := discoverOpenClaudeModels(context.Background(), Command{Path: writeOpenClaudeTestExecutable(t)})
	if err != nil {
		t.Fatalf("discoverOpenClaudeModels: %v", err)
	}
	if len(models) != 1 || models[0].ID != "profile-model" || !models[0].Default {
		t.Fatalf("models = %#v, want profile-model", models)
	}
}

func writeOpenClaudeTestExecutable(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "openclaude")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("write fake openclaude: %v", err)
	}
	return path
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func openClaudeTestResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}
