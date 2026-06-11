package cmd

import (
	"context"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/ops"
	"github.com/astra-sh/qvr/internal/ops/discover"
	"github.com/astra-sh/qvr/internal/ops/store"
	"github.com/astra-sh/qvr/internal/output"
	"github.com/astra-sh/qvr/internal/ui"
	"github.com/spf13/cobra"
)

var (
	uiHost       string
	uiPort       int
	uiNoOpen     bool
	uiGlobal     bool
	uiAll        bool
	uiNoDiscover bool
	uiBuild      bool
)

// uiScanInterval is how often a running `qvr ui` rescans the agents' session
// stores, so the dashboard tracks new sessions live. Scans are stat-diff
// incremental, so an idle interval costs a directory walk and ledger lookups.
const uiScanInterval = 30 * time.Second

var uiCmd = &cobra.Command{
	Use:   "ui",
	Short: "Launch the local dashboard (sessions, skills, tree, scan, provenance)",
	Long: `Start a local web dashboard for Quiver — a local view over what the
CLI already records: agent sessions from the audit pipeline, installed skills,
the registry → skill → target tree, scan results, and provenance.

On launch the dashboard scans your agents' native session stores in the
background and keeps rescanning while it runs, so sessions and analytics stay
live without manual refreshes (pass --no-discover to turn the scans off). A
previous qvr ui holding the port is replaced automatically, so you always get
the binary you just launched; --build additionally rebuilds the React bundle
and serves it from disk (dev convenience, requires the qvr repo).

The dashboard is bound to ` + "`127.0.0.1`" + ` by design — installs, updates, and
scan gates stay in the CLI, and it is not a remote server. It is served from
the single binary; the React bundle is embedded at build time. If the bundle
hasn't been built, the API still runs and the page explains how to build it
(` + "`make ui`" + `).`,
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
	uiCmd.Flags().BoolVar(&uiNoDiscover, "no-discover", false,
		"skip the background session-store scans (launch + live tracking)")
	uiCmd.Flags().BoolVar(&uiBuild, "build", false,
		"rebuild the dashboard bundle (npm run build) and serve it from disk — dev convenience, requires the qvr repo")
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

	var diskAssets fs.FS
	if uiBuild {
		if diskAssets, err = buildDashboardBundle(projectRoot); err != nil {
			return err
		}
	}

	// The dashboard discovers by default: opening it means wanting the data,
	// and the scan only reads what agents already wrote. Create+migrate the
	// audit DB before the read-only server handle opens, so the launch scan
	// (and the dashboard's discover button) have a live store from the first
	// request. --no-discover opts out.
	if !uiNoDiscover {
		if err := ensureAuditDB(ctx); err != nil {
			return err
		}
	}

	srv, err := buildUIServer(ctx, projectRoot, uiGlobal, uiAll, version)
	if err != nil {
		return err
	}
	defer func() { _ = srv.Close() }()
	srv.assets = diskAssets

	if !uiNoDiscover {
		go runDiscoverLoop(ctx)
	}

	addr := net.JoinHostPort(uiHost, fmt.Sprintf("%d", uiPort))
	ln, err := listenReplacingPrevious(addr)
	if err != nil {
		return err
	}
	pidPath := writeUIPidFile(uiPort)
	defer func() { _ = os.Remove(pidPath) }()

	url := fmt.Sprintf("http://%s/", ln.Addr().String())
	printUILaunchInfo(srv, diskAssets != nil, url)

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

// printUILaunchInfo writes the listening banner and any setup warnings.
func printUILaunchInfo(srv *uiServer, diskAssets bool, url string) {
	style := output.NewStyler(os.Stderr)
	fmt.Fprintf(os.Stderr, "Quiver UI listening on %s\n", url)
	if !srv.hasAudit() {
		fmt.Fprintf(os.Stderr, "%s audit DB not found — sessions will be empty; run `qvr audit discover` (or relaunch without --no-discover)\n",
			style.BoldYellow("warning:"))
	}
	if !diskAssets && !ui.HasIndex() {
		fmt.Fprintf(os.Stderr, "%s dashboard bundle not built — run `make ui`; the API is live\n",
			style.BoldYellow("warning:"))
	}
	fmt.Fprintln(os.Stderr, style.Dim("Press Ctrl+C to stop"))
}

// uiPidPath is where a running `qvr ui` records its pid, per port, so the
// next launch can replace it instead of failing with "address in use".
func uiPidPath(port int) string {
	return filepath.Join(config.Dir(), fmt.Sprintf("ui-%d.pid", port))
}

// writeUIPidFile best-effort records this process as the port's dashboard.
func writeUIPidFile(port int) string {
	p := uiPidPath(port)
	_ = os.WriteFile(p, []byte(strconv.Itoa(os.Getpid())), 0o600)
	return p
}

// listenReplacingPrevious binds addr; when the port is held by a previous
// `qvr ui` (identified by its pid file — never an arbitrary process), that
// instance is told to shut down and the bind is retried, so launching the
// dashboard always serves the binary you just ran.
func listenReplacingPrevious(addr string) (net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err == nil {
		return ln, nil
	}

	_, portStr, splitErr := net.SplitHostPort(addr)
	if splitErr != nil {
		return nil, fmt.Errorf("listen on %s: %w", addr, err)
	}
	port, _ := strconv.Atoi(portStr)
	pid := previousUIPid(port)
	if pid <= 0 || pid == os.Getpid() {
		return nil, fmt.Errorf("listen on %s: %w (held by another process — stop it or pass --port)", addr, err)
	}

	fmt.Fprintf(os.Stderr, "replacing previous qvr ui (pid %d) on %s\n", pid, addr)
	if proc, ferr := os.FindProcess(pid); ferr == nil {
		_ = proc.Signal(syscall.SIGTERM)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(150 * time.Millisecond)
		if ln, err = net.Listen("tcp", addr); err == nil {
			return ln, nil
		}
	}
	return nil, fmt.Errorf("listen on %s: %w (previous qvr ui pid %d did not exit)", addr, err, pid)
}

// previousUIPid identifies a prior `qvr ui` holding port: the pid file first
// (written by every dashboard since the takeover behavior shipped), then —
// for instances that predate it — the OS's listener table, accepted only when
// the owning process is itself a qvr binary. Returns 0 when the holder isn't
// ours to replace.
func previousUIPid(port int) int {
	if data, err := os.ReadFile(uiPidPath(port)); err == nil {
		if pid, perr := strconv.Atoi(strings.TrimSpace(string(data))); perr == nil && pid > 0 {
			return pid
		}
	}
	if runtime.GOOS == "windows" {
		return 0
	}
	out, err := exec.Command("lsof", "-nP", "-ti", fmt.Sprintf("tcp:%d", port), "-sTCP:LISTEN").Output()
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0]))
	if err != nil || pid <= 0 {
		return 0
	}
	comm, err := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
	if err != nil || filepath.Base(strings.TrimSpace(string(comm))) != "qvr" {
		return 0
	}
	return pid
}

// buildDashboardBundle rebuilds the React bundle and returns a disk-backed
// filesystem over the fresh dist, so `qvr ui --build` serves the latest UI
// without recompiling the binary. Dev convenience: requires running inside
// the qvr repo (ui/package.json present) with Node available.
func buildDashboardBundle(projectRoot string) (fs.FS, error) {
	uiDir := filepath.Join(projectRoot, "ui")
	if _, err := os.Stat(filepath.Join(uiDir, "package.json")); err != nil {
		return nil, fmt.Errorf("--build needs the qvr repo (no ui/package.json under %s)", projectRoot)
	}
	fmt.Fprintln(os.Stderr, "building dashboard bundle (npm run build)…")
	build := exec.Command("npm", "run", "build")
	build.Dir = uiDir
	build.Stdout = os.Stderr
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		return nil, fmt.Errorf("npm run build: %w", err)
	}
	dist := filepath.Join(projectRoot, "internal", "ui", "dist")
	if _, err := os.Stat(filepath.Join(dist, "index.html")); err != nil {
		return nil, fmt.Errorf("bundle built but %s/index.html is missing: %w", dist, err)
	}
	return os.DirFS(dist), nil
}

// hasAudit reports whether the audit store was opened (DB present).
func (s *uiServer) hasAudit() bool { return s.store != nil }

// ensureAuditDB creates + migrates the audit database if it doesn't exist yet
// (a brief read-write open), so a subsequent read-only open succeeds.
func ensureAuditDB(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	st, err := store.Open(ctx, store.OpenOptions{Path: ops.DBPath(cfg)})
	if err != nil {
		return fmt.Errorf("create audit database: %w", err)
	}
	return st.Close()
}

// runDiscoverLoop keeps the dashboard live: an immediate scan on launch, then
// an incremental rescan every uiScanInterval for as long as the server runs,
// so new agent sessions appear without manual refreshes. Each pass uses its
// own read-write store handle (the server's handle stays read-only; SQLite
// WAL lets the reader see the writer's commits).
func runDiscoverLoop(ctx context.Context) {
	runDiscoverPass(ctx, true)
	ticker := time.NewTicker(uiScanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runDiscoverPass(ctx, false)
		}
	}
}

// runDiscoverPass performs one scan. Only the launch pass reports an
// all-unchanged outcome; the steady-state ticks stay silent unless something
// was ingested or failed.
func runDiscoverPass(ctx context.Context, verbose bool) {
	// Same in-process guard as POST /api/discover: concurrent scans would
	// double-ingest the files both saw as new.
	discoverScanMu.Lock()
	defer discoverScanMu.Unlock()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "discover: %v\n", err)
		return
	}
	st, err := store.Open(ctx, store.OpenOptions{Path: ops.DBPath(cfg)})
	if err != nil {
		fmt.Fprintf(os.Stderr, "discover: %v\n", err)
		return
	}
	defer func() { _ = st.Close() }()

	rep, err := discover.Scan(ctx, st, discover.Options{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "discover: %v\n", err)
		return
	}
	t := rep.Totals()
	if verbose || t.Ingested > 0 || t.Errors > 0 {
		fmt.Fprintf(os.Stderr, "discover: %d sessions recorded, %d skill-less skipped, %d unchanged\n",
			t.Ingested, t.Skipped, t.Unchanged)
	}
}

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
