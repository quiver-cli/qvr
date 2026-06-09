package cmd

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/astra-sh/qvr/internal/output"
	"github.com/astra-sh/qvr/internal/ui"
	"github.com/spf13/cobra"
)

var (
	uiHost   string
	uiPort   int
	uiNoOpen bool
	uiGlobal bool
	uiAll    bool
)

var uiCmd = &cobra.Command{
	Use:   "ui",
	Short: "Launch the local dashboard (sessions, skills, tree, scan, provenance)",
	Long: `Start a local web dashboard for Quiver — a local view over what the
CLI already records: agent sessions from the audit pipeline, installed skills,
the registry → skill → target tree, scan results, and provenance.

The dashboard is read-only and bound to ` + "`127.0.0.1`" + ` by design — it never
mutates state (installs, updates, and scan gates stay in the CLI) and is not a
remote server. It is served from the single binary; the React bundle is embedded
at build time. If the bundle hasn't been built, the API still runs and the page
explains how to build it (` + "`make ui`" + `).`,
	Args: cobra.NoArgs,
	RunE: runUI,
}

func init() {
	uiCmd.Flags().StringVar(&uiHost, "host", "127.0.0.1",
		"host to bind (kept local by design; not intended for remote exposure)")
	uiCmd.Flags().IntVar(&uiPort, "port", 7878, "port to listen on")
	uiCmd.Flags().BoolVar(&uiNoOpen, "no-open", false, "do not open a browser window")
	uiCmd.Flags().BoolVar(&uiGlobal, "global", false,
		"read the user-global lock instead of the project lock")
	uiCmd.Flags().BoolVar(&uiAll, "all", false,
		"union project and global locks (adds a scope column)")
	rootCmd.AddCommand(uiCmd)
}

func runUI(cmd *cobra.Command, args []string) error {
	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	srv, err := buildUIServer(ctx, projectRoot, uiGlobal, uiAll, version)
	if err != nil {
		return err
	}
	defer func() { _ = srv.Close() }()

	addr := net.JoinHostPort(uiHost, fmt.Sprintf("%d", uiPort))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}

	style := output.NewStyler(os.Stderr)
	url := fmt.Sprintf("http://%s/", ln.Addr().String())
	fmt.Fprintf(os.Stderr, "Quiver UI listening on %s\n", url)
	if !srv.hasAudit() {
		fmt.Fprintf(os.Stderr, "%s audit DB not found — sessions will be empty; run `qvr audit enable`\n",
			style.BoldYellow("warning:"))
	}
	if !ui.HasIndex() {
		fmt.Fprintf(os.Stderr, "%s dashboard bundle not built — run `make ui`; the API is live\n",
			style.BoldYellow("warning:"))
	}
	fmt.Fprintln(os.Stderr, style.Dim("Press Ctrl+C to stop"))

	if !uiNoOpen {
		go openBrowser(url)
	}

	httpSrv := &http.Server{
		Handler:           srv.handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

// hasAudit reports whether the audit store was opened (DB present).
func (s *uiServer) hasAudit() bool { return s.store != nil }

// openBrowser best-effort opens the default browser. Failure is non-fatal — the
// URL is already printed, so the user can open it manually.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default: // linux, bsd, etc.
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
