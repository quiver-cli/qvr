package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/astra-sh/qvr/internal/output"
	"github.com/astra-sh/qvr/internal/selfupdate"
)

// captureUpgrade wires the package-global printer to buffers and returns them so
// tests can assert on stdout (json/info) and stderr separately.
func captureUpgrade(t *testing.T, format output.Format) (out, errBuf *bytes.Buffer) {
	t.Helper()
	out = &bytes.Buffer{}
	errBuf = &bytes.Buffer{}
	prev := printer
	printer = &output.Printer{Out: out, Err: errBuf, Format: format}
	t.Cleanup(func() { printer = prev })
	return out, errBuf
}

// resetUpgradeFlags returns the upgrade flag globals to their defaults so tests
// don't leak state into one another.
func resetUpgradeFlags(t *testing.T) {
	t.Helper()
	prevV, prevC, prevY, prevF := upgradeVersion, upgradeCheck, upgradeYes, upgradeForce
	upgradeVersion, upgradeCheck, upgradeYes, upgradeForce = "", false, false, false
	t.Cleanup(func() {
		upgradeVersion, upgradeCheck, upgradeYes, upgradeForce = prevV, prevC, prevY, prevF
	})
}

// withVersion overrides the build-stamp `version` var for the duration of a test.
func withVersion(t *testing.T, v string) {
	t.Helper()
	prev := version
	version = v
	t.Cleanup(func() { version = prev })
}

// fakeUpdaterServer stands up a GitHub-shaped server and points newUpdater at it.
func fakeUpdaterServer(t *testing.T, tag, binContents string) {
	t.Helper()
	asset, err := selfupdate.AssetName(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skipf("unsupported test platform: %v", err)
	}
	var archBuf bytes.Buffer
	gz := gzip.NewWriter(&archBuf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "qvr", Mode: 0o755, Size: int64(len(binContents)), Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte(binContents))
	_ = tw.Close()
	_ = gz.Close()
	archive := archBuf.Bytes()
	sum := sha256.Sum256(archive)
	checksums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), asset)

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+selfupdate.Repo+"/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"tag_name": %q}`, tag)
	})
	mux.HandleFunc("/"+selfupdate.Repo+"/releases/download/"+tag+"/"+asset, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(archive)
	})
	mux.HandleFunc("/"+selfupdate.Repo+"/releases/download/"+tag+"/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(checksums))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	prev := newUpdater
	newUpdater = func() *selfupdate.Updater {
		return &selfupdate.Updater{HTTP: srv.Client(), APIBase: srv.URL, DownloadBase: srv.URL, Repo: selfupdate.Repo}
	}
	t.Cleanup(func() { newUpdater = prev })
}

func TestUpgrade_AlreadyUpToDate(t *testing.T) {
	resetUpgradeFlags(t)
	withVersion(t, "v0.11.2")
	fakeUpdaterServer(t, "v0.11.2", "ignored")
	out, _ := captureUpgrade(t, output.FormatText)

	if err := runUpgrade(upgradeCmd, nil); err != nil {
		t.Fatalf("runUpgrade: %v", err)
	}
	if !strings.Contains(out.String(), "Already up to date") {
		t.Errorf("want 'Already up to date', got %q", out.String())
	}
}

func TestUpgrade_CheckReportsAvailable(t *testing.T) {
	resetUpgradeFlags(t)
	upgradeCheck = true
	withVersion(t, "v0.10.0")
	fakeUpdaterServer(t, "v0.11.2", "ignored")
	out, _ := captureUpgrade(t, output.FormatText)

	if err := runUpgrade(upgradeCmd, nil); err != nil {
		t.Fatalf("runUpgrade: %v", err)
	}
	s := out.String()
	if !strings.Contains(s, "Update available") || !strings.Contains(s, "v0.11.2") {
		t.Errorf("want update-available message mentioning v0.11.2, got %q", s)
	}
}

func TestUpgrade_CheckJSON(t *testing.T) {
	resetUpgradeFlags(t)
	upgradeCheck = true
	withVersion(t, "v0.10.0")
	fakeUpdaterServer(t, "v0.11.2", "ignored")
	out, _ := captureUpgrade(t, output.FormatJSON)

	if err := runUpgrade(upgradeCmd, nil); err != nil {
		t.Fatalf("runUpgrade: %v", err)
	}
	var res upgradeResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("decode json: %v (raw %q)", err, out.String())
	}
	if res.Current != "v0.10.0" || res.Latest != "v0.11.2" || res.Updated {
		t.Errorf("unexpected result %+v", res)
	}
	if res.Asset == "" {
		t.Errorf("asset should be populated, got empty")
	}
}

func TestUpgrade_NonTTYRefusesWithoutYes(t *testing.T) {
	resetUpgradeFlags(t)
	withVersion(t, "v0.10.0")
	fakeUpdaterServer(t, "v0.11.2", "ignored")
	captureUpgrade(t, output.FormatText)

	if runtime.GOOS == "windows" {
		t.Skip("fakeUpdaterServer serves a tar.gz; the Windows .zip path is covered in selfupdate_test")
	}
	prevTTY := stdinIsTTYFn
	stdinIsTTYFn = func() bool { return false }
	t.Cleanup(func() { stdinIsTTYFn = prevTTY })

	err := runUpgrade(upgradeCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("want refusal hinting at --yes, got %v", err)
	}
}

func TestUpgrade_PerformsSwapWithYes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fakeUpdaterServer serves a tar.gz; the Windows .zip path is covered in selfupdate_test")
	}
	resetUpgradeFlags(t)
	upgradeYes = true
	withVersion(t, "v0.10.0")
	fakeUpdaterServer(t, "v0.11.2", "FRESH-BINARY")
	out, _ := captureUpgrade(t, output.FormatText)

	// Point os.Executable at a temp "running binary" so the real swap runs
	// without clobbering the test binary.
	dir := t.TempDir()
	target := filepath.Join(dir, "qvr")
	if err := os.WriteFile(target, []byte("OLD"), 0o755); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	prevExe := osExecutable
	osExecutable = func() (string, error) { return target, nil }
	t.Cleanup(func() { osExecutable = prevExe })

	if err := runUpgrade(upgradeCmd, nil); err != nil {
		t.Fatalf("runUpgrade: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "FRESH-BINARY" {
		t.Errorf("target = %q, want FRESH-BINARY after swap", got)
	}
	if !strings.Contains(out.String(), "Upgraded") {
		t.Errorf("want 'Upgraded' confirmation, got %q", out.String())
	}
}
