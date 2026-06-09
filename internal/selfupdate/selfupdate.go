// Package selfupdate replaces the running qvr binary in place with a newer
// release downloaded from GitHub Releases. It is the Go port of install.sh: it
// resolves the latest published version, downloads the OS/arch-specific archive
// plus checksums.txt, verifies the sha256, unpacks the binary, and atomically
// swaps it over the currently-running executable.
//
// Because the React dashboard is embedded into every release binary at build
// time (see .goreleaser.yaml), pulling the latest release binary also pulls the
// latest UI — there is no separate UI artifact to fetch.
//
// The asset naming MUST stay in lockstep with .goreleaser.yaml's archive
// name_template and install.sh's ASSET construction: qvr_<Os>_<Arch>.tar.gz
// where Os is title-cased (Darwin/Linux/Windows) and Arch is x86_64/arm64.
package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Repo is the GitHub "owner/name" the binaries are published under. It mirrors
// install.sh's REPO and .goreleaser.yaml's release.github block — the single
// place to change if the project ever moves again.
const Repo = "astra-sh/qvr"

const (
	// defaultAPIBase is GitHub's REST API host (releases metadata).
	defaultAPIBase = "https://api.github.com"
	// defaultDownloadBase is the release-asset host (the tarballs themselves).
	defaultDownloadBase = "https://github.com"
	// binaryName is the executable inside the archive and on disk.
	binaryName = "qvr"
)

// Updater performs version resolution, download/verify, and atomic replace.
// APIBase and DownloadBase are injectable so tests can point at an httptest
// server; production callers use New() which wires the real GitHub hosts.
type Updater struct {
	HTTP         *http.Client
	APIBase      string // e.g. https://api.github.com
	DownloadBase string // e.g. https://github.com
	Repo         string // owner/name
	// Token, when non-empty, is sent as a Bearer credential. Public-repo
	// installs need none; it exists to dodge unauthenticated rate limits and
	// to support private forks. Sourced from GITHUB_TOKEN / GH_TOKEN by New().
	Token string
}

// New returns an Updater pointed at the real GitHub hosts, honoring a
// GITHUB_TOKEN / GH_TOKEN in the environment if present.
func New() *Updater {
	return &Updater{
		HTTP:         &http.Client{Timeout: 60 * time.Second},
		APIBase:      defaultAPIBase,
		DownloadBase: defaultDownloadBase,
		Repo:         Repo,
		Token:        firstNonEmptyEnv("GITHUB_TOKEN", "GH_TOKEN"),
	}
}

func firstNonEmptyEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

// AssetName returns the release archive filename for the given os/arch, matching
// .goreleaser.yaml's name_template (title-cased OS, x86_64/arm64). Exposed so
// callers and tests can assert the exact asset without duplicating the mapping.
func AssetName(goos, goarch string) (string, error) {
	osPart, err := osToken(goos)
	if err != nil {
		return "", err
	}
	archPart, err := archToken(goarch)
	if err != nil {
		return "", err
	}
	ext := "tar.gz"
	if goos == "windows" {
		// goreleaser format_overrides packages Windows as a .zip.
		ext = "zip"
	}
	return fmt.Sprintf("%s_%s_%s.%s", binaryName, osPart, archPart, ext), nil
}

func osToken(goos string) (string, error) {
	switch goos {
	case "linux":
		return "Linux", nil
	case "darwin":
		return "Darwin", nil
	case "windows":
		return "Windows", nil
	default:
		return "", fmt.Errorf("unsupported OS %q", goos)
	}
}

func archToken(goarch string) (string, error) {
	switch goarch {
	case "amd64":
		return "x86_64", nil
	case "arm64":
		return "arm64", nil
	default:
		return "", fmt.Errorf("unsupported architecture %q", goarch)
	}
}

// LatestVersion resolves the tag_name of the repo's latest published release
// (e.g. "v0.11.2"). Returns a clear error when no release exists — the exact
// state that broke #177 — so callers can tell "no release yet" from a transport
// failure.
func (u *Updater) LatestVersion(ctx context.Context) (string, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/latest", strings.TrimRight(u.APIBase, "/"), u.Repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	u.authorize(req)

	resp, err := u.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("query latest release: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("no published release found for %s (set --version to pin one)", u.Repo)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("query latest release: unexpected status %s", resp.Status)
	}

	var payload struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode latest release: %w", err)
	}
	if payload.TagName == "" {
		return "", fmt.Errorf("latest release for %s has no tag_name", u.Repo)
	}
	return payload.TagName, nil
}

// DownloadBinary fetches the archive for version+os/arch, verifies its sha256
// against the release's checksums.txt, unpacks the qvr binary into dstDir, and
// returns the path to the extracted (executable) binary. The caller is
// responsible for moving it into place (see Replace).
func (u *Updater) DownloadBinary(ctx context.Context, version, goos, goarch, dstDir string) (string, error) {
	asset, err := AssetName(goos, goarch)
	if err != nil {
		return "", err
	}
	base := fmt.Sprintf("%s/%s/releases/download/%s", strings.TrimRight(u.DownloadBase, "/"), u.Repo, version)

	archive, err := u.fetch(ctx, base+"/"+asset)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", asset, err)
	}
	checksums, err := u.fetch(ctx, base+"/checksums.txt")
	if err != nil {
		return "", fmt.Errorf("download checksums.txt: %w", err)
	}

	if err := verifyChecksum(archive, checksums, asset); err != nil {
		return "", err
	}

	binPath, err := extractBinary(archive, asset, dstDir, goos)
	if err != nil {
		return "", err
	}
	return binPath, nil
}

// Replace atomically swaps newBin over the running executable at targetPath.
// It never overwrites the live binary directly: it writes a sibling temp file
// in the target directory and renames it into place (a plain overwrite of a
// running binary corrupts its code-signing vnode on macOS — the same reason
// install.sh uses temp-file + rename). On Windows a running .exe cannot be
// renamed over, so the current binary is moved aside first.
func Replace(targetPath, newBin string) error {
	dir := filepath.Dir(targetPath)
	tmp, err := os.CreateTemp(dir, "."+binaryName+".tmp.*")
	if err != nil {
		return fmt.Errorf("create temp in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }() // no-op once renamed away

	src, err := os.Open(newBin)
	if err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := io.Copy(tmp, src); err != nil {
		_ = src.Close()
		_ = tmp.Close()
		return fmt.Errorf("stage new binary: %w", err)
	}
	_ = src.Close()
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return fmt.Errorf("chmod staged binary: %w", err)
	}

	if runtime.GOOS == "windows" {
		// A running .exe is locked; move it aside so the rename can land. The
		// stale copy is best-effort removed on the next run.
		old := targetPath + ".old"
		_ = os.Remove(old)
		if err := os.Rename(targetPath, old); err != nil {
			return fmt.Errorf("move running binary aside: %w", err)
		}
		// Restore old binary if the final rename fails.
		defer func() {
			if _, statErr := os.Stat(targetPath); os.IsNotExist(statErr) {
				_ = os.Rename(old, targetPath)
			}
		}()
	}

	if err := os.Rename(tmpPath, targetPath); err != nil {
		return fmt.Errorf("install new binary over %s: %w", targetPath, err)
	}
	return nil
}

// authorize attaches the bearer token when one is configured.
func (u *Updater) authorize(req *http.Request) {
	if u.Token != "" {
		req.Header.Set("Authorization", "Bearer "+u.Token)
	}
}

// fetch GETs url and returns the full body, following redirects (the default
// client does) so GitHub's asset CDN hops resolve transparently.
func (u *Updater) fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	u.authorize(req)
	resp, err := u.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %s for %s", resp.Status, url)
	}
	return io.ReadAll(resp.Body)
}

// verifyChecksum confirms sha256(archive) matches the line for asset in a
// GoReleaser checksums.txt ("<hex>  <filename>" per line).
func verifyChecksum(archive, checksums []byte, asset string) error {
	sum := sha256.Sum256(archive)
	got := hex.EncodeToString(sum[:])

	want := ""
	for line := range strings.SplitSeq(string(checksums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == asset {
			want = fields[0]
			break
		}
	}
	if want == "" {
		return fmt.Errorf("checksums.txt has no entry for %s", asset)
	}
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("checksum mismatch for %s (want %s, got %s)", asset, want, got)
	}
	return nil
}

// archiveBinaryName is the name the qvr executable carries inside the release
// archive: GoReleaser appends .exe on Windows builds, so the zip holds qvr.exe
// while the tarballs hold a bare qvr.
func archiveBinaryName(goos string) string {
	if goos == "windows" {
		return binaryName + ".exe"
	}
	return binaryName
}

// extractBinary unpacks the qvr executable from a release archive into dstDir
// and returns its path. It dispatches on the archive extension — .tar.gz for
// linux/macOS, .zip for windows (GoReleaser's format_overrides) — so upgrade is
// pure Go on every platform with no external tar/unzip dependency.
func extractBinary(archive []byte, asset, dstDir, goos string) (string, error) {
	switch {
	case strings.HasSuffix(asset, ".tar.gz"):
		return extractFromTarGz(archive, asset, dstDir, goos)
	case strings.HasSuffix(asset, ".zip"):
		return extractFromZip(archive, asset, dstDir, goos)
	default:
		return "", fmt.Errorf("unsupported archive format for %s", asset)
	}
}

// writeStaged copies r into a fresh executable file in dstDir and returns its
// path. The staged filename is irrelevant to the final install (Replace copies
// the contents over the real target), so it always uses the bare binary name.
func writeStaged(dstDir string, r io.Reader) (string, error) {
	dst := filepath.Join(dstDir, binaryName)
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, r); err != nil { //nolint:gosec // archive is checksum-verified before extraction
		_ = out.Close()
		return "", fmt.Errorf("extract %s: %w", binaryName, err)
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	return dst, nil
}

func extractFromTarGz(archive []byte, asset, dstDir, goos string) (string, error) {
	want := archiveBinaryName(goos)
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return "", fmt.Errorf("open gzip: %w", err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read archive: %w", err)
		}
		if filepath.Base(hdr.Name) != want || hdr.Typeflag != tar.TypeReg {
			continue
		}
		return writeStaged(dstDir, tr)
	}
	return "", fmt.Errorf("archive %s did not contain %s", asset, want)
}

func extractFromZip(archive []byte, asset, dstDir, goos string) (string, error) {
	want := archiveBinaryName(goos)
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return "", fmt.Errorf("open zip: %w", err)
	}
	for _, f := range zr.File {
		if filepath.Base(f.Name) != want || f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", fmt.Errorf("open %s in archive: %w", want, err)
		}
		dst, err := writeStaged(dstDir, rc)
		_ = rc.Close()
		if err != nil {
			return "", err
		}
		return dst, nil
	}
	return "", fmt.Errorf("archive %s did not contain %s", asset, want)
}
