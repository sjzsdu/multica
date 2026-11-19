package main

import "testing"

func TestSingleWorkspaceIDFromDaemonHealth(t *testing.T) {
	tests := []struct {
		name   string
		health map[string]any
		want   string
	}{
		{
			name: "one workspace",
			health: map[string]any{"workspaces": []any{
				map[string]any{"id": " e214e2b6-cf68-4ed0-9b5e-5b8bc0b22d91 "},
			}},
			want: "e214e2b6-cf68-4ed0-9b5e-5b8bc0b22d91",
		},
		{
			name: "multiple workspaces stays ambiguous",
			health: map[string]any{"workspaces": []any{
				map[string]any{"id": "one"},
				map[string]any{"id": "two"},
			}},
			want: "",
		},
		{
			name:   "missing workspaces",
			health: map[string]any{},
			want:   "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := singleWorkspaceIDFromDaemonHealth(tt.health); got != tt.want {
				t.Fatalf("singleWorkspaceIDFromDaemonHealth() = %q, want %q", got, tt.want)
			}
		})
	}
}
