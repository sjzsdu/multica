package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

type localWebState struct {
	FrontendURL string    `json:"frontend_url"`
	BackendURL  string    `json:"backend_url"`
	LocalAuth   bool      `json:"local_auth"`
	PID         int       `json:"pid"`
	UpdatedAt   time.Time `json:"updated_at"`
}

var localCmd = &cobra.Command{
	Use:   "local",
	Short: "Manage the local Multica web session",
}

var localStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the active local web session",
	RunE:  runLocalStatus,
}

var localLoginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate the CLI against the active local web session",
	RunE:  runLocalLogin,
}

func init() {
	localCmd.AddCommand(localStatusCmd)
	localCmd.AddCommand(localLoginCmd)
}

func localWebStatePath() (string, error) {
	dir, err := cli.ProfileDir("")
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "local-web.json"), nil
}

func saveLocalWebState(state localWebState) error {
	path, err := localWebStatePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	state.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func loadLocalWebState() (localWebState, error) {
	path, err := localWebStatePath()
	if err != nil {
		return localWebState{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return localWebState{}, errors.New("no local web session found; run `multica web` first")
		}
		return localWebState{}, err
	}
	var state localWebState
	if err := json.Unmarshal(data, &state); err != nil {
		return localWebState{}, err
	}
	return state, nil
}

func ensureDaemonAuthFromLocalState(cmd *cobra.Command, profile string) error {
	cfg, err := cli.LoadCLIConfigForProfile(profile)
	state, stateErr := loadLocalWebState()
	if stateErr != nil {
		if err == nil && cfg.Token != "" {
			return nil
		}
		return nil
	}
	if err == nil && cfg.Token != "" && strings.TrimRight(cfg.ServerURL, "/") == strings.TrimRight(state.BackendURL, "/") {
		return nil
	}
	if !state.LocalAuth || state.BackendURL == "" || !probeLocalBackend(state.BackendURL) {
		return nil
	}
	fmt.Fprintln(os.Stderr, "Local web session detected; authenticating daemon locally...")
	return ensureLocalCLIAuth(cmd, state.BackendURL, state.FrontendURL)
}

func runLocalStatus(cmd *cobra.Command, _ []string) error {
	state, err := loadLocalWebState()
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Frontend: %s\n", valueOrDefault(state.FrontendURL, "(unknown)"))
	fmt.Fprintf(cmd.OutOrStdout(), "Backend:  %s\n", valueOrDefault(state.BackendURL, "(unknown)"))
	fmt.Fprintf(cmd.OutOrStdout(), "Auth:     %s\n", map[bool]string{true: "local", false: "normal"}[state.LocalAuth])
	fmt.Fprintf(cmd.OutOrStdout(), "PID:      %d\n", state.PID)
	fmt.Fprintf(cmd.OutOrStdout(), "Updated:  %s\n", state.UpdatedAt.Format(time.RFC3339))
	return nil
}

func runLocalLogin(cmd *cobra.Command, _ []string) error {
	state, err := loadLocalWebState()
	if err != nil {
		return err
	}
	if strings.TrimSpace(state.BackendURL) == "" {
		return errors.New("local web state has no backend URL; restart `multica web`")
	}
	if !state.LocalAuth {
		return errors.New("local auth is not enabled for this web session; set MULTICA_LOCAL_AUTH=true and restart `multica web`")
	}
	if err := waitForLocalBackend(state.BackendURL, 30*time.Second); err != nil {
		return err
	}
	return ensureLocalCLIAuth(cmd, state.BackendURL, state.FrontendURL)
}

func ensureLocalCLIAuth(cmd *cobra.Command, backendURL, frontendURL string) error {
	backendURL = strings.TrimRight(backendURL, "/")
	frontendURL = strings.TrimRight(frontendURL, "/")
	client := cli.NewAPIClient(backendURL, "", "")
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var loginResp struct {
		Token string `json:"token"`
		User  struct {
			Email string `json:"email"`
			Name  string `json:"name"`
		} `json:"user"`
	}
	if err := client.PostJSON(ctx, "/auth/local", map[string]any{}, &loginResp); err != nil {
		return cli.WithUserMessage("Local login failed: the local backend did not accept /auth/local. Check MULTICA_LOCAL_AUTH=true and restart `multica web`.", err)
	}
	if loginResp.Token == "" {
		return errors.New("local login did not return a token")
	}

	jwtClient := cli.NewAPIClient(backendURL, "", loginResp.Token)
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown"
	}
	var patResp struct {
		Token string `json:"token"`
	}
	if err := jwtClient.PostJSON(ctx, "/api/tokens", map[string]any{
		"name":            fmt.Sprintf("Local CLI (%s)", hostname),
		"expires_in_days": 90,
	}, &patResp); err != nil {
		return cli.WithUserMessage("Local login failed: could not create a CLI access token.", err)
	}
	if err := validateLoginTokenPrefix(patResp.Token); err != nil {
		return err
	}

	patClient := cli.NewAPIClient(backendURL, "", patResp.Token)
	var me struct {
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	if err := patClient.GetJSON(ctx, "/api/me", &me); err != nil {
		return cli.WithUserMessage("Local login failed: generated token was not accepted.", err)
	}

	profile := resolveProfile(cmd)
	cfg, _ := cli.LoadCLIConfigForProfile(profile)
	cfg.Token = patResp.Token
	cfg.ServerURL = backendURL
	cfg.AppURL = frontendURL
	if err := cli.SaveCLIConfigForProfile(cfg, profile); err != nil {
		return fmt.Errorf("save CLI config: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Authenticated locally as %s (%s)\n", valueOrDefault(me.Name, "local"), me.Email)
	return nil
}

func localFlagEnabled(value string) bool {
	value = strings.TrimSpace(value)
	return strings.EqualFold(value, "1") || strings.EqualFold(value, "true") || strings.EqualFold(value, "yes") || strings.EqualFold(value, "on")
}

func probeLocalBackend(url string) bool {
	client := http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Get(strings.TrimRight(url, "/") + "/api/config")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 500
}
