package cmd

import (
	"context"
	"fmt"
	"os"
	"runtime"

	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/output"
	"github.com/astra-sh/qvr/internal/selfupdate"
	"github.com/spf13/cobra"
)

var (
	upgradeVersion string
	upgradeCheck   bool
	upgradeYes     bool
	upgradeForce   bool
)

// newUpdater is a package-level seam: production returns a real GitHub-backed
// updater; tests override it to point at an httptest server (mirrors the
// stdinIsTTYFn seam used by `qvr cache`).
var newUpdater = func() *selfupdate.Updater { return selfupdate.New() }

// osExecutable resolves the path of the running binary to replace. It is a seam
// so tests can target a temp file instead of clobbering the test binary.
var osExecutable = os.Executable

var upgradeCmd = &cobra.Command{
	Use:   "upgrade",
	Short: "Update qvr in place to the latest release",
	Long: `Download the latest qvr release for this OS/arch and replace the running
binary in place.

The release binary ships with the React dashboard embedded, so upgrading also
brings the UI (` + "`qvr ui`" + `) up to date — there is no separate UI download.

The archive's sha256 is verified against the release's checksums.txt before the
binary is swapped, and the swap is atomic (a sibling temp file renamed into
place) so an interrupted upgrade never leaves a half-written executable.

Use ` + "`--check`" + ` to see whether a newer release exists without installing it,
or ` + "`--version vX.Y.Z`" + ` to pin a specific release.`,
	Args: cobra.NoArgs,
	RunE: runUpgrade,
}

func init() {
	upgradeCmd.Flags().StringVar(&upgradeVersion, "version", "",
		"install this exact release tag (e.g. v0.11.2) instead of the latest")
	upgradeCmd.Flags().BoolVar(&upgradeCheck, "check", false,
		"report whether a newer release is available, then exit without installing")
	upgradeCmd.Flags().BoolVarP(&upgradeYes, "yes", "y", false,
		"skip the confirmation prompt")
	upgradeCmd.Flags().BoolVar(&upgradeForce, "force", false,
		"reinstall even if already on the target version")
	rootCmd.AddCommand(upgradeCmd)
}

// upgradeResult is the --output json payload.
type upgradeResult struct {
	Current string `json:"current"`
	Latest  string `json:"latest"`
	Asset   string `json:"asset"`
	Updated bool   `json:"updated"`
}

func runUpgrade(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	up := newUpdater()

	// Resolve the target release: an explicit --version, else the latest.
	target := upgradeVersion
	if target == "" {
		latest, err := up.LatestVersion(ctx)
		if err != nil {
			return err
		}
		target = latest
	}

	asset, err := selfupdate.AssetName(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}

	current := version // the cmd.version ldflag ("dev" for un-stamped builds)
	// A dev/unstamped build has no comparable version — always treat it as
	// "needs the release" rather than silently claiming it's up to date.
	isDev := !model.IsSemverTag(current)
	upToDate := !isDev && model.CompareSemver(current, target) >= 0

	res := upgradeResult{Current: current, Latest: target, Asset: asset}

	if upToDate && !upgradeForce {
		res.Updated = false
		if printer.Format == output.FormatJSON {
			return printer.JSON(res)
		}
		printer.Info(fmt.Sprintf("already up to date (%s)", current))
		return nil
	}

	// --check: report the gap and stop before downloading anything.
	if upgradeCheck {
		return reportUpgradeCheck(res, current, target, isDev)
	}

	// Confirm before mutating the on-disk binary (reuses the cache-prune gate).
	if !upgradeYes {
		if !stdinIsTTY() {
			return fmt.Errorf("refusing to upgrade without confirmation; re-run with --yes")
		}
		prompt := fmt.Sprintf("Upgrade %s → %s? [y/N] ", describeCurrent(current, isDev), target)
		if !confirmYesNo(prompt) {
			printer.Info("upgrade cancelled")
			return nil
		}
	}

	return downloadAndReplace(ctx, up, res, target, asset, current, isDev)
}

// downloadAndReplace fetches the target release binary into a temp dir, swaps it
// atomically over the running executable, and reports success (JSON or text).
func downloadAndReplace(ctx context.Context, up *selfupdate.Updater, res upgradeResult, target, asset, current string, isDev bool) error {
	exe, err := osExecutable()
	if err != nil {
		return fmt.Errorf("locate running binary: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "qvr-upgrade-")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	printer.Info(fmt.Sprintf("Downloading %s (%s)...", target, asset))
	newBin, err := up.DownloadBinary(ctx, target, runtime.GOOS, runtime.GOARCH, tmpDir)
	if err != nil {
		return err
	}

	if err := selfupdate.Replace(exe, newBin); err != nil {
		return err
	}

	res.Updated = true
	if printer.Format == output.FormatJSON {
		return printer.JSON(res)
	}
	printer.Success(fmt.Sprintf("upgraded %s → %s", describeCurrent(current, isDev), target))
	printer.Info(fmt.Sprintf("  installed: %s", exe))
	printer.Info("  the embedded dashboard (`qvr ui`) is now current too")
	return nil
}

// reportUpgradeCheck implements `qvr upgrade --check`: report the version gap
// (JSON payload or a human line distinguishing a dev build from a release) and
// return without downloading anything.
func reportUpgradeCheck(res upgradeResult, current, target string, isDev bool) error {
	if printer.Format == output.FormatJSON {
		return printer.JSON(res)
	}
	if isDev {
		printer.Info(fmt.Sprintf("dev build (%s) — latest release is %s; run `qvr upgrade` to install it", current, target))
	} else {
		printer.Info(fmt.Sprintf("update available: %s → %s (run `qvr upgrade` to install)", current, target))
	}
	return nil
}

// describeCurrent renders the running version for human messages, labelling an
// un-stamped build plainly instead of printing "dev" as if it were a release.
func describeCurrent(current string, isDev bool) string {
	if isDev {
		return "dev build"
	}
	return current
}
