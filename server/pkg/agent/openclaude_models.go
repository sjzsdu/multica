package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// OpenClaude does not expose a stable machine-readable `openclaude models`
// command. Its /model picker discovers models from the active provider: the
// OpenAI-compatible providers expose /models, while Ollama exposes /api/tags.
// Keep discovery here rather than making the Claude backend guess a static
// catalog, so the runtime's provider profile remains the source of truth.
func discoverOpenClaudeModels(ctx context.Context, runtimeCmd Command) ([]Model, error) {
	if runtimeCmd.Path == "" {
		runtimeCmd.Path = "openclaude"
	}
	if _, err := exec.LookPath(runtimeCmd.Path); err != nil {
		return []Model{}, nil
	}

	env, profile := openClaudeDiscoveryEnv()
	configured := openClaudeConfiguredModels(env)
	baseURL := openClaudeBaseURL(env, profile)

	var discovered []string
	if profile == "ollama" || strings.Contains(strings.ToLower(baseURL), "11434") {
		discovered = openClaudeOllamaModels(ctx, openClaudeOllamaBaseURL(baseURL))
	} else if baseURL != "" {
		discovered = openClaudeHTTPModels(ctx, baseURL, openClaudeAPIHeaders(env, profile))
	}

	models := openClaudeModelEntries(discovered, configured)
	return models, nil
}

func openClaudeDiscoveryEnv() (map[string]string, string) {
	env := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			env[key] = value
		}
	}

	profileName, profileEnv := readOpenClaudeProfile()
	for key, value := range profileEnv {
		if strings.TrimSpace(env[key]) == "" {
			env[key] = value
		}
	}
	return env, profileName
}

func readOpenClaudeProfile() (string, map[string]string) {
	paths := make([]string, 0, 3)
	if configDir := strings.TrimSpace(os.Getenv("OPENCLAUDE_CONFIG_DIR")); configDir != "" {
		paths = append(paths, filepath.Join(configDir, ".openclaude-profile.json"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths,
			filepath.Join(home, ".openclaude", ".openclaude-profile.json"),
			filepath.Join(home, ".openclaude-profile.json"),
		)
	}

	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var profile struct {
			Profile string            `json:"profile"`
			Env     map[string]string `json:"env"`
		}
		if err := json.Unmarshal(data, &profile); err != nil || profile.Env == nil {
			continue
		}
		return strings.ToLower(strings.TrimSpace(profile.Profile)), profile.Env
	}
	return "", nil
}

func openClaudeValue(env map[string]string, key string) string {
	return strings.TrimSpace(env[key])
}

func openClaudeBaseURL(env map[string]string, profile string) string {
	if value := openClaudeValue(env, "OPENAI_BASE_URL"); value != "" {
		return strings.TrimRight(value, "/")
	}
	if value := openClaudeValue(env, "OPENAI_API_BASE"); value != "" {
		return strings.TrimRight(value, "/")
	}
	if value := openClaudeValue(env, "GEMINI_BASE_URL"); value != "" {
		return strings.TrimRight(value, "/")
	}
	if value := openClaudeValue(env, "ANTHROPIC_BASE_URL"); value != "" {
		return strings.TrimRight(value, "/")
	}
	if value := openClaudeValue(env, "MINIMAX_BASE_URL"); value != "" {
		return strings.TrimRight(value, "/")
	}
	if value := openClaudeValue(env, "MISTRAL_BASE_URL"); value != "" {
		return strings.TrimRight(value, "/")
	}
	if value := openClaudeValue(env, "CONCENTRATE_BASE_URL"); value != "" {
		return strings.TrimRight(value, "/")
	}
	if value := openClaudeValue(env, "BANKR_BASE_URL"); value != "" {
		return strings.TrimRight(value, "/")
	}

	switch profile {
	case "ollama":
		return "http://localhost:11434"
	case "atomic-chat":
		return "http://127.0.0.1:1337"
	case "gemini":
		return "https://generativelanguage.googleapis.com/v1beta/openai"
	case "mistral":
		return "https://api.mistral.ai/v1"
	case "opencode":
		return "https://opencode.ai/zen/v1"
	case "anthropic":
		return "https://api.anthropic.com/v1"
	case "openai":
		return "https://api.openai.com/v1"
	default:
		return ""
	}
}

func openClaudeOllamaBaseURL(baseURL string) string {
	baseURL = strings.TrimRight(baseURL, "/")
	baseURL = strings.TrimSuffix(baseURL, "/v1")
	if baseURL == "" {
		return "http://localhost:11434"
	}
	return baseURL
}

func openClaudeAPIHeaders(env map[string]string, profile string) map[string]string {
	headers := map[string]string{}
	if header := openClaudeValue(env, "OPENAI_AUTH_HEADER"); header != "" {
		if value := openClaudeValue(env, "OPENAI_AUTH_HEADER_VALUE"); value != "" {
			headers[header] = value
			return headers
		}
	}

	keyNames := []string{
		"OPENAI_API_KEY", "OPENCODE_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY",
		"MISTRAL_API_KEY", "MINIMAX_API_KEY", "NVIDIA_API_KEY", "AIMLAPI_API_KEY",
		"MIMO_API_KEY", "APISMART_API_KEY", "NEARAI_API_KEY", "FIREWORKS_API_KEY",
		"LONGCAT_API_KEY", "LLMTR_API_KEY", "CONCENTRATE_API_KEY", "CLINE_API_KEY",
		"CLOUDFLARE_API_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN",
	}
	if profile == "anthropic" {
		keyNames = []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"}
	}
	for _, keyName := range keyNames {
		if key := openClaudeValue(env, keyName); key != "" {
			if profile == "anthropic" || strings.HasPrefix(strings.ToLower(keyName), "anthropic_") {
				headers["x-api-key"] = key
				headers["anthropic-version"] = "2023-06-01"
			} else {
				headers["Authorization"] = "Bearer " + key
			}
			return headers
		}
	}
	return headers
}

func openClaudeHTTPModels(ctx context.Context, baseURL string, headers map[string]string) []string {
	endpoint := strings.TrimRight(baseURL, "/")
	if !strings.HasSuffix(endpoint, "/models") {
		endpoint += "/models"
	}
	requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := openClaudeHTTPClient.Do(request)
	if err != nil {
		return nil
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil
	}
	var payload struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return nil
	}
	models := make([]string, 0, len(payload.Data))
	for _, model := range payload.Data {
		if id := strings.TrimSpace(model.ID); id != "" {
			models = append(models, id)
		}
	}
	return uniqueStrings(models)
}

var openClaudeHTTPClient = http.DefaultClient

func openClaudeOllamaModels(ctx context.Context, baseURL string) []string {
	endpoint := strings.TrimRight(baseURL, "/") + "/api/tags"
	requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil
	}
	response, err := openClaudeHTTPClient.Do(request)
	if err != nil {
		return nil
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil
	}
	var payload struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&payload) != nil {
		return nil
	}
	models := make([]string, 0, len(payload.Models))
	for _, model := range payload.Models {
		if name := strings.TrimSpace(model.Name); name != "" {
			models = append(models, name)
		}
	}
	return uniqueStrings(models)
}

func openClaudeConfiguredModels(env map[string]string) []string {
	keys := []string{
		"MULTICA_OPENCLAUDE_MODEL", "OPENCLAUDE_MODEL", "OPENAI_MODEL", "GEMINI_MODEL",
		"MISTRAL_MODEL", "MINIMAX_MODEL", "BANKR_MODEL", "APISMART_MODEL",
		"CONCENTRATE_MODEL", "NVIDIA_MODEL", "ANTHROPIC_MODEL",
	}
	for _, key := range keys {
		value := openClaudeValue(env, key)
		if value == "" {
			continue
		}
		parts := strings.Split(value, ",")
		models := make([]string, 0, len(parts))
		for _, part := range parts {
			if model := strings.TrimSpace(part); model != "" {
				models = append(models, model)
			}
		}
		return uniqueStrings(models)
	}
	return nil
}

func openClaudeModelEntries(discovered, configured []string) []Model {
	ids := uniqueStrings(append(append([]string(nil), discovered...), configured...))
	if len(ids) == 0 {
		return []Model{}
	}
	configuredSet := make(map[string]struct{}, len(configured))
	for _, id := range configured {
		configuredSet[id] = struct{}{}
	}
	models := make([]Model, 0, len(ids))
	for _, id := range ids {
		_, isDefault := configuredSet[id]
		models = append(models, Model{
			ID:       id,
			Label:    id,
			Provider: "openclaude",
			Default:  isDefault,
		})
	}
	return models
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	return unique
}
