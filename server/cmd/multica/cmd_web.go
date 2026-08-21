package main

import (
	"bufio"
	"context"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

// webAssets embeds the standalone Next.js frontend bundle copied by
// `make build-web-embed`.
//
//go:embed all:web_dist
var webAssets embed.FS

type embeddedWebOptions struct {
	Addr           string
	OpenBrowser    bool
	APIURL         string
	BackendCommand string
	Local          bool
	KeepDaemon     bool
	ConfigAPIURL   string
}

type localWebBackend struct {
	Enabled   bool
	Command   string
	WorkDir   string
	Env       []string
	APIURL    string
	LocalAuth bool
}

var webCmd = &cobra.Command{
	Use:   "web",
	Short: "Start the bundled Multica web UI",
	Long: "Start the bundled Multica web UI. By default Multica picks a free " +
		"loopback port, points the UI at the configured CLI server when one exists, " +
		"and opens the browser.",
	RunE: func(cmd *cobra.Command, args []string) error {
		addr, _ := cmd.Flags().GetString("addr")
		openBrowser, _ := cmd.Flags().GetBool("open")
		apiURL, _ := cmd.Flags().GetString("api-url")
		backendCommand, _ := cmd.Flags().GetString("backend-command")
		local, _ := cmd.Flags().GetBool("local")
		keepDaemon, _ := cmd.Flags().GetBool("daemon")
		configAPIURL := ""
		if apiURL == "" {
			configAPIURL = tryResolveWebAPIURL(cmd)
		}
		return runEmbeddedWebWithOptions(embeddedWebOptions{
			Addr:           addr,
			OpenBrowser:    openBrowser,
			APIURL:         apiURL,
			BackendCommand: backendCommand,
			Local:          local,
			KeepDaemon:     keepDaemon,
			ConfigAPIURL:   configAPIURL,
		})
	},
}

var webStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the local web daemon session",
	RunE:  runWebStatus,
}

var webStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the local web daemon session",
	RunE:  runWebStop,
}

var webRestartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Restart the local web daemon session using the current multica binary",
	RunE:  runWebRestart,
}

func init() {
	webCmd.Flags().String("addr", "127.0.0.1:0", "address for the bundled web UI; use port 0 to pick a free port")
	webCmd.Flags().Bool("open", true, "open the web UI in the default browser")
	webCmd.Flags().String("api-url", "", "backend API origin for the web UI (defaults to --server-url, MULTICA_SERVER_URL, or config server_url when set)")
	webCmd.Flags().String("backend-command", "", "optional command to start before the web UI, such as 'server/bin/server' or 'make server'")
	webCmd.Flags().Bool("local", true, "when a local .env and server source are present, prepare PostgreSQL, run migrations, and start the backend automatically")
	webCmd.Flags().Bool("daemon", false, "run the full local web stack in the background")
	webCmd.AddCommand(webStatusCmd, webStopCmd, webRestartCmd)
}

func runEmbeddedWeb(addr string, openBrowser bool) error {
	return runEmbeddedWebWithOptions(embeddedWebOptions{Addr: addr, OpenBrowser: openBrowser, Local: false})
}

func startEmbeddedWebDaemon(options embeddedWebOptions) error {
	if err := stopLocalWebSession(5 * time.Second); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not stop previous local web session: %v\n", err)
	}
	exe := currentExecutablePath()
	if exe == "" {
		return errors.New("could not resolve current multica binary path")
	}
	profileDir, err := localWebProfileDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		return err
	}
	logPath := filepath.Join(profileDir, "local-web.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()

	args := []string{"web", "--addr", options.Addr, "--open=false"}
	if options.APIURL != "" {
		args = append(args, "--api-url", options.APIURL)
	}
	if options.BackendCommand != "" {
		args = append(args, "--backend-command", options.BackendCommand)
	}
	if !options.Local {
		args = append(args, "--local=false")
	}
	args = append(args, "--daemon")

	cmd := exec.Command(exe, args...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = upsertEnv(upsertEnv(os.Environ(), "MULTICA_WEB_FOREGROUND", "1"), "MULTICA_WEB_LOG", logPath)
	cmd.SysProcAttr = detachedProcessAttrs()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start local web daemon: %w", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Process.Release(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Multica local web daemon starting (pid %d)\n", pid)
	fmt.Fprintf(os.Stderr, "Logs: %s\n", logPath)
	state, err := waitForWebDaemonState(pid, 45*time.Second)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Frontend: %s\n", state.FrontendURL)
	fmt.Fprintf(os.Stdout, "Backend:  %s\n", state.BackendURL)
	return nil
}

func runWebStatus(cmd *cobra.Command, _ []string) error {
	state, err := loadLocalWebState()
	if err != nil {
		return err
	}
	running := state.PID > 0 && processAlive(state.PID)
	fmt.Fprintf(cmd.OutOrStdout(), "Status:   %s\n", map[bool]string{true: "running", false: "stopped"}[running])
	fmt.Fprintf(cmd.OutOrStdout(), "Frontend: %s\n", valueOrDefault(state.FrontendURL, "(unknown)"))
	fmt.Fprintf(cmd.OutOrStdout(), "Backend:  %s\n", valueOrDefault(state.BackendURL, "(unknown)"))
	fmt.Fprintf(cmd.OutOrStdout(), "PID:      %d\n", state.PID)
	fmt.Fprintf(cmd.OutOrStdout(), "Mode:     %s\n", valueOrDefault(state.Mode, "foreground"))
	if state.BinaryPath != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Binary:   %s\n", state.BinaryPath)
	}
	if state.Version != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Version:  %s\n", state.Version)
	}
	if state.LogPath != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Logs:     %s\n", state.LogPath)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Updated:  %s\n", state.UpdatedAt.Format(time.RFC3339))
	return nil
}

func runWebStop(cmd *cobra.Command, _ []string) error {
	if err := stopLocalWebSession(10 * time.Second); err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), "Multica local web stopped")
	return nil
}

func runWebRestart(cmd *cobra.Command, _ []string) error {
	if err := stopLocalWebSession(10 * time.Second); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not stop previous local web session: %v\n", err)
	}
	return startEmbeddedWebDaemon(embeddedWebOptions{Addr: "127.0.0.1:0", OpenBrowser: false, Local: true, KeepDaemon: true})
}

func waitForWebDaemonState(pid int, timeout time.Duration) (localWebState, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		state, err := loadLocalWebState()
		if err == nil && state.PID == pid && state.FrontendURL != "" && state.BackendURL != "" && probeLocalBackend(state.BackendURL) && probeWebFrontend(state.FrontendURL) {
			return state, nil
		}
		if !processAlive(pid) {
			return localWebState{}, errors.New("local web daemon exited during startup; check `multica web status` logs")
		}
		time.Sleep(500 * time.Millisecond)
	}
	return localWebState{}, errors.New("local web daemon did not become ready in time; check `multica web status` logs")
}

func probeWebFrontend(url string) bool {
	client := http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Get(strings.TrimRight(url, "/"))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 500
}

func stopLocalWebSession(timeout time.Duration) error {
	state, err := loadLocalWebState()
	if err != nil {
		return nil
	}
	_ = runDaemonStop(daemonStopCmd, nil)
	if state.PID <= 0 || !processAlive(state.PID) {
		return nil
	}
	if err := signalProcess(state.PID, syscall.SIGTERM); err != nil {
		return err
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processAlive(state.PID) {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return signalProcess(state.PID, syscall.SIGKILL)
}

func localWebProfileDir() (string, error) {
	path, err := localWebStatePath()
	if err != nil {
		return "", err
	}
	return filepath.Dir(path), nil
}

func currentExecutablePath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		return resolved
	}
	return exe
}

func webStateMode(detached bool) string {
	if detached {
		return "daemon"
	}
	return "foreground"
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	return err == nil
}

func signalProcess(pid int, sig os.Signal) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Signal(sig)
}

func runEmbeddedWebWithOptions(options embeddedWebOptions) error {
	if options.KeepDaemon && os.Getenv("MULTICA_WEB_FOREGROUND") != "1" {
		return startEmbeddedWebDaemon(options)
	}
	assets, err := fs.Sub(webAssets, "web_dist")
	if err != nil {
		return err
	}
	if !embeddedWebBundlePresent(assets) {
		return errors.New("embedded web bundle is missing; run `make build-web-embed` before building the multica binary")
	}

	nodePath, err := exec.LookPath("node")
	if err != nil {
		return errors.New("Node.js is required to run the bundled web UI. Install Node.js 22 or newer, then run `multica web` again")
	}

	webAddr, err := resolveWebListenAddr(options.Addr)
	if err != nil {
		return err
	}
	localBackend, err := prepareLocalWebBackend(options)
	if err != nil {
		return err
	}
	if localBackend.Enabled && options.APIURL == "" {
		options.APIURL = localBackend.APIURL
	}
	if localBackend.Enabled && options.BackendCommand == "" {
		options.BackendCommand = localBackend.Command
	}
	if options.APIURL == "" {
		options.APIURL = options.ConfigAPIURL
	}
	if options.APIURL != "" {
		options.APIURL, err = normalizeWebAPIURL(options.APIURL)
		if err != nil {
			return err
		}
	}

	dir, err := os.MkdirTemp("", "multica-web-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if err := extractEmbeddedWeb(assets, dir); err != nil {
		return err
	}

	nodePathDirs, err := embeddedWebNodePath(filepath.Join(dir, "node_modules", ".pnpm"))
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	backendCmd, err := startWebBackendCommand(ctx, options.BackendCommand, localBackend.WorkDir, localBackend.Env)
	if err != nil {
		return err
	}
	if backendCmd != nil && backendCmd.Process != nil {
		defer func() {
			_ = backendCmd.Process.Signal(os.Interrupt)
			_, _ = backendCmd.Process.Wait()
		}()
	}

	cmd := exec.CommandContext(ctx, nodePath, "server.js")
	cmd.Dir = filepath.Join(dir, "apps", "web")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = embeddedWebEnv(os.Environ(), webAddr, options.APIURL, nodePathDirs)

	url := webURL(webAddr)
	if localBackend.Enabled {
		state := localWebState{
			FrontendURL: url,
			BackendURL:  options.APIURL,
			LocalAuth:   localBackend.LocalAuth,
			PID:         os.Getpid(),
			Mode:        webStateMode(options.KeepDaemon),
			BinaryPath:  currentExecutablePath(),
			Version:     version,
			WorkDir:     localBackend.WorkDir,
			LogPath:     os.Getenv("MULTICA_WEB_LOG"),
		}
		if err := saveLocalWebState(state); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not write local web state: %v\n", err)
		}
		if err := waitForLocalBackend(options.APIURL, 30*time.Second); err != nil {
			return err
		}
		if state.LocalAuth {
			if err := ensureLocalCLIAuth(rootCmd, options.APIURL, url); err != nil {
				return err
			}
		}
		// Avoid stale-port leaks: a daemon from a previous web session may still be
		// running against an old random backend port. Stop it before starting the
		// daemon for this session so runtime heartbeats point at the current backend.
		_ = runDaemonStop(daemonStopCmd, nil)
		if err := runDaemonStart(daemonStartCmd, nil); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not start daemon automatically: %v\n", err)
		} else if !options.KeepDaemon {
			defer func() { _ = runDaemonStop(daemonStopCmd, nil) }()
		}
	}
	if options.OpenBrowser {
		go func() {
			time.Sleep(500 * time.Millisecond)
			_ = openURL(url)
		}()
	}

	fmt.Printf("Multica web UI listening on %s\n", url)
	if options.APIURL != "" {
		fmt.Printf("Backend API: %s\n", options.APIURL)
	} else {
		fmt.Println("Backend API: same origin (set --api-url or --server-url to proxy to a backend)")
	}
	if backendCmd != nil {
		fmt.Printf("Backend command started: %s\n", options.BackendCommand)
	}
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	return nil
}

func waitForLocalBackend(url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if probeLocalBackend(url) {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("local backend did not become ready at %s within %s", url, timeout)
}

func embeddedWebBundlePresent(assets fs.FS) bool {
	info, err := fs.Stat(assets, "apps/web/server.js")
	return err == nil && !info.IsDir()
}

func resolveWebListenAddr(raw string) (string, error) {
	host, port, err := net.SplitHostPort(raw)
	if err != nil {
		return "", fmt.Errorf("invalid --addr %q: %w", raw, err)
	}
	if host == "" {
		host = "127.0.0.1"
	}
	if port == "0" {
		listener, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
		if err != nil {
			return "", err
		}
		_, selectedPort, err := net.SplitHostPort(listener.Addr().String())
		closeErr := listener.Close()
		if err != nil {
			return "", err
		}
		if closeErr != nil {
			return "", closeErr
		}
		port = selectedPort
	}
	return net.JoinHostPort(host, port), nil
}

func normalizeWebAPIURL(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("invalid --api-url %q: expected an http(s) URL", raw)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	if strings.HasSuffix(parsed.Path, "/api") {
		parsed.Path = strings.TrimSuffix(parsed.Path, "/api")
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func tryResolveWebAPIURL(cmd *cobra.Command) string {
	if val := tryResolveExplicitServerURL(cmd); val != "" {
		return val
	}
	if inDaemonManagedExecutionContext() && strings.TrimSpace(os.Getenv("MULTICA_TASK_CONFIG_ROOT")) == "" {
		return ""
	}
	return tryResolveProfileServerURL(cmd)
}

func prepareLocalWebBackend(options embeddedWebOptions) (localWebBackend, error) {
	if !options.Local || options.APIURL != "" || options.BackendCommand != "" {
		return localWebBackend{}, nil
	}
	root, ok := discoverLocalWebProject(".")
	if !ok {
		return localWebBackend{}, nil
	}
	envFile := filepath.Join(root, ".env")
	env, vars, err := envFromDotEnv(envFile)
	if err != nil {
		return localWebBackend{}, err
	}
	fmt.Printf("Local .env detected: %s\n", envFile)
	fmt.Println("Preparing local PostgreSQL and migrations...")
	if err := runWebSetupCommand(root, env, "bash", "scripts/ensure-postgres.sh", ".env"); err != nil {
		return localWebBackend{}, err
	}
	if err := runWebSetupCommand(filepath.Join(root, "server"), env, "go", "run", "./cmd/migrate", "up"); err != nil {
		return localWebBackend{}, err
	}
	port := strings.TrimSpace(vars["PORT"])
	if port == "" {
		port = "8080"
	}
	port, err = resolveLocalBackendPort(port)
	if err != nil {
		return localWebBackend{}, err
	}
	env = upsertEnv(env, "PORT", port)
	return localWebBackend{
		Enabled:   true,
		Command:   "go run ./cmd/server",
		WorkDir:   filepath.Join(root, "server"),
		Env:       env,
		APIURL:    "http://127.0.0.1:" + port,
		LocalAuth: localFlagEnabled(vars["MULTICA_LOCAL_AUTH"]),
	}, nil
}

func resolveLocalBackendPort(preferred string) (string, error) {
	preferred = strings.TrimSpace(preferred)
	if preferred == "" {
		preferred = "8080"
	}
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", preferred))
	if err == nil {
		_ = listener.Close()
		return preferred, nil
	}

	listener, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer listener.Close()
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		return "", err
	}
	fmt.Printf("Backend port %s is busy; using %s for this multica web session.\n", preferred, port)
	return port, nil
}

func discoverLocalWebProject(start string) (string, bool) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", false
	}
	for {
		if fileExists(filepath.Join(dir, ".env")) && fileExists(filepath.Join(dir, "server", "cmd", "server", "main.go")) && fileExists(filepath.Join(dir, "scripts", "ensure-postgres.sh")) {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func envFromDotEnv(path string) ([]string, map[string]string, error) {
	vars, err := parseDotEnvFile(path)
	if err != nil {
		return nil, nil, err
	}
	env := os.Environ()
	for key, value := range vars {
		env = upsertEnv(env, key, value)
	}
	return env, vars, nil
}

func parseDotEnvFile(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	vars := map[string]string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || strings.ContainsAny(key, " \t") {
			continue
		}
		if unquoted, err := strconv.Unquote(value); err == nil {
			value = unquoted
		}
		vars[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return vars, nil
}

func runWebSetupCommand(dir string, env []string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

func embeddedWebEnv(base []string, addr, apiURL string, nodePathDirs []string) []string {
	host, port, _ := net.SplitHostPort(addr)
	env := append([]string{}, base...)
	env = upsertEnv(env, "HOSTNAME", host)
	env = upsertEnv(env, "PORT", port)
	env = upsertEnv(env, "NODE_ENV", "production")
	env = upsertEnv(env, "NODE_PATH", strings.Join(nodePathDirs, string(os.PathListSeparator)))
	if apiURL != "" {
		env = upsertEnv(env, "REMOTE_API_URL", apiURL)
		env = upsertEnv(env, "NEXT_PUBLIC_API_URL", "")
	}
	return env
}

func upsertEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, item := range env {
		if strings.HasPrefix(item, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

func webURL(addr string) string {
	host, port, _ := net.SplitHostPort(addr)
	if host == "0.0.0.0" || host == "::" || host == "" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

func extractEmbeddedWeb(assets fs.FS, dst string) error {
	return fs.WalkDir(assets, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dst, filepath.FromSlash(path))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		in, err := assets.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	})
}

func embeddedWebNodePath(pnpmDir string) ([]string, error) {
	entries, err := os.ReadDir(pnpmDir)
	if err != nil {
		return nil, err
	}
	dirs := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(pnpmDir, entry.Name(), "node_modules")
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			dirs = append(dirs, dir)
		}
	}
	return dirs, nil
}

func startWebBackendCommand(ctx context.Context, command, workDir string, env []string) (*exec.Cmd, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil, nil
	}
	parts, err := splitShellLike(command)
	if err != nil {
		return nil, err
	}
	if len(parts) == 0 {
		return nil, nil
	}
	cmd := exec.CommandContext(ctx, parts[0], parts[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if workDir != "" {
		cmd.Dir = workDir
	}
	if env != nil {
		cmd.Env = env
	} else {
		cmd.Env = os.Environ()
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start --backend-command: %w", err)
	}
	return cmd, nil
}

func splitShellLike(command string) ([]string, error) {
	var parts []string
	var current strings.Builder
	var quote rune
	escaped := false
	for _, r := range command {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				current.WriteRune(r)
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			continue
		}
		if r == ' ' || r == '\t' || r == '\n' {
			if current.Len() > 0 {
				parts = append(parts, current.String())
				current.Reset()
			}
			continue
		}
		current.WriteRune(r)
	}
	if escaped {
		current.WriteRune('\\')
	}
	if quote != 0 {
		return nil, errors.New("unterminated quote in --backend-command")
	}
	if current.Len() > 0 {
		parts = append(parts, current.String())
	}
	return parts, nil
}

func openURL(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	cmd.Args = removeEmptyArgs(cmd.Args)
	return cmd.Start()
}

func removeEmptyArgs(args []string) []string {
	filtered := args[:0]
	for _, arg := range args {
		if strings.TrimSpace(arg) != "" {
			filtered = append(filtered, arg)
		}
	}
	return filtered
}
