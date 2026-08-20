package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
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
		if apiURL == "" {
			apiURL = tryResolveWebAPIURL(cmd)
		}
		return runEmbeddedWebWithOptions(embeddedWebOptions{
			Addr:           addr,
			OpenBrowser:    openBrowser,
			APIURL:         apiURL,
			BackendCommand: backendCommand,
		})
	},
}

func init() {
	webCmd.Flags().String("addr", "127.0.0.1:0", "address for the bundled web UI; use port 0 to pick a free port")
	webCmd.Flags().Bool("open", true, "open the web UI in the default browser")
	webCmd.Flags().String("api-url", "", "backend API origin for the web UI (defaults to --server-url, MULTICA_SERVER_URL, or config server_url when set)")
	webCmd.Flags().String("backend-command", "", "optional command to start before the web UI, such as 'server/bin/server' or 'make server'")
}

func runEmbeddedWeb(addr string, openBrowser bool) error {
	return runEmbeddedWebWithOptions(embeddedWebOptions{Addr: addr, OpenBrowser: openBrowser})
}

func runEmbeddedWebWithOptions(options embeddedWebOptions) error {
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

	backendCmd, err := startWebBackendCommand(ctx, options.BackendCommand)
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

func startWebBackendCommand(ctx context.Context, command string) (*exec.Cmd, error) {
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
	cmd.Env = os.Environ()
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
