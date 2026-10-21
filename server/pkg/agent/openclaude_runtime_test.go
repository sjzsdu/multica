package agent

import (
	"testing"
)

func TestOpenClaudeRuntimeDescriptor(t *testing.T) {
	desc, ok := BuiltinRuntimeByID("openclaude")
	if !ok {
		t.Fatal("openclaude descriptor not found")
	}

	want := map[string]string{
		"ID":                "openclaude",
		"ProtocolFamily":    "claude",
		"DefaultCommand":    "openclaude",
		"EnvPrefix":         "MULTICA_OPENCLAUDE",
		"DisplayName":       "OpenClaude",
		"SkillsDir":         ".openclaude/skills",
		"UserSkillsDir":     ".openclaude/skills",
		"LaunchHeader":      "openclaude (stream-json)",
		"DefaultExecutable": "openclaude",
		"ProviderLabel":     "openclaude",
	}
	got := map[string]string{
		"ID":                desc.ID,
		"ProtocolFamily":    desc.ProtocolFamily,
		"DefaultCommand":    desc.DefaultCommand,
		"EnvPrefix":         desc.EnvPrefix,
		"DisplayName":       desc.DisplayName,
		"SkillsDir":         desc.SkillsDir,
		"UserSkillsDir":     desc.UserSkillsDir,
		"LaunchHeader":      desc.LaunchHeader,
		"DefaultExecutable": desc.DefaultExecutable,
		"ProviderLabel":     desc.ProviderLabel,
	}
	for field, wantValue := range want {
		if got[field] != wantValue {
			t.Errorf("%s = %q, want %q", field, got[field], wantValue)
		}
	}
	if desc.ModelDiscovery == nil {
		t.Fatal("openclaude should discover models from its active provider catalog")
	}
}

func TestResolveOpenClaudeUsesClaudeStreamBackendWithOwnDefaults(t *testing.T) {
	backend, err := ResolveBackend("openclaude", Config{})
	if err != nil {
		t.Fatalf("ResolveBackend(openclaude): %v", err)
	}

	claude, ok := backend.(*claudeBackend)
	if !ok {
		t.Fatalf("ResolveBackend(openclaude) = %T, want *claudeBackend", backend)
	}
	if claude.defaultExecutable != "openclaude" {
		t.Fatalf("defaultExecutable = %q, want openclaude", claude.defaultExecutable)
	}
	if claude.providerLabel != "openclaude" {
		t.Fatalf("providerLabel = %q, want openclaude", claude.providerLabel)
	}
}
