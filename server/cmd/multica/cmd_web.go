package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
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

var webCmd = &cobra.Command{
	Use:   "web",
	Short: "Start the bundled Multica web UI",
	RunE: func(cmd *cobra.Command, args []string) error {
		addr, _ := cmd.Flags().GetString("addr")
		openBrowser, _ := cmd.Flags().GetBool("open")
		return runEmbeddedWeb(addr, openBrowser)
	},
}

func init() {
	webCmd.Flags().String("addr", "127.0.0.1:3000", "address for the bundled web UI")
	webCmd.Flags().Bool("open", true, "open the web UI in the default browser")
}

func runEmbeddedWeb(addr string, openBrowser bool) error {
	assets, err := fs.Sub(webAssets, "web_dist")
	if err != nil {
		return err
	}
	if !embeddedWebBundlePresent(assets) {
		return errors.New("embedded web bundle is missing; run `make build-web-embed` before building the multica binary")
	}

	nodePath, err := exec.LookPath("node")
	if err != nil {
		return errors.New("node is required to run the bundled web UI; install Node.js and try again")
	}

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid --addr %q: %w", addr, err)
	}
	if host == "" {
		host = "127.0.0.1"
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

	cmd := exec.CommandContext(ctx, nodePath, "server.js")
	cmd.Dir = filepath.Join(dir, "apps", "web")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(),
		"HOSTNAME="+host,
		"PORT="+port,
		"NODE_ENV=production",
		"NODE_PATH="+strings.Join(nodePathDirs, string(os.PathListSeparator)),
	)

	urlHost := host
	if urlHost == "0.0.0.0" || urlHost == "::" {
		urlHost = "127.0.0.1"
	}
	url := "http://" + net.JoinHostPort(urlHost, port)
	if openBrowser {
		go func() {
			time.Sleep(500 * time.Millisecond)
			_ = openURL(url)
		}()
	}

	fmt.Printf("Multica web UI listening on %s\n", url)
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
