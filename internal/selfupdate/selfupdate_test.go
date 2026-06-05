package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAssetName(t *testing.T) {
	tests := []struct {
		goos, goarch, want string
		wantErr            bool
	}{
		{"darwin", "arm64", "qvr_Darwin_arm64.tar.gz", false},
		{"darwin", "amd64", "qvr_Darwin_x86_64.tar.gz", false},
		{"linux", "amd64", "qvr_Linux_x86_64.tar.gz", false},
		{"linux", "arm64", "qvr_Linux_arm64.tar.gz", false},
		{"windows", "amd64", "qvr_Windows_x86_64.zip", false},
		{"plan9", "amd64", "", true},
		{"linux", "mips", "", true},
	}
	for _, tt := range tests {
		got, err := AssetName(tt.goos, tt.goarch)
		if tt.wantErr {
			if err == nil {
				t.Errorf("AssetName(%s,%s): want error, got %q", tt.goos, tt.goarch, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("AssetName(%s,%s): unexpected error %v", tt.goos, tt.goarch, err)
		}
		if got != tt.want {
			t.Errorf("AssetName(%s,%s) = %q, want %q", tt.goos, tt.goarch, got, tt.want)
		}
	}
}

// makeTarGz builds a .tar.gz containing a single regular file named `qvr` with
// the given contents, returning the archive bytes.
func makeTarGz(t *testing.T, binContents string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{Name: "qvr", Mode: 0o755, Size: int64(len(binContents)), Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("tar header: %v", err)
	}
	if _, err := tw.Write([]byte(binContents)); err != nil {
		t.Fatalf("tar write: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// newFakeRelease stands up an httptest server that serves the latest-release
// JSON, the archive, and a checksums.txt for the running OS/arch. It returns an
// Updater pointed at it (APIBase==DownloadBase==server URL since the test
// server multiplexes both path shapes) plus the asset name and archive bytes.
func newFakeRelease(t *testing.T, tag, binContents string) (*Updater, string, []byte) {
	t.Helper()
	asset, err := AssetName(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skipf("unsupported test platform: %v", err)
	}
	archive := makeTarGz(t, binContents)
	checksums := fmt.Sprintf("%s  %s\n", sha256hex(archive), asset)

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+Repo+"/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"tag_name": %q}`, tag)
	})
	mux.HandleFunc("/"+Repo+"/releases/download/"+tag+"/"+asset, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(archive)
	})
	mux.HandleFunc("/"+Repo+"/releases/download/"+tag+"/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(checksums))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	up := &Updater{
		HTTP:         srv.Client(),
		APIBase:      srv.URL,
		DownloadBase: srv.URL,
		Repo:         Repo,
	}
	return up, asset, archive
}

func TestLatestVersion(t *testing.T) {
	up, _, _ := newFakeRelease(t, "v0.11.2", "binary-bytes")
	got, err := up.LatestVersion(context.Background())
	if err != nil {
		t.Fatalf("LatestVersion: %v", err)
	}
	if got != "v0.11.2" {
		t.Errorf("LatestVersion = %q, want v0.11.2", got)
	}
}

func TestLatestVersion_NoRelease(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+Repo+"/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	up := &Updater{HTTP: srv.Client(), APIBase: srv.URL, DownloadBase: srv.URL, Repo: Repo}

	_, err := up.LatestVersion(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no published release") {
		t.Fatalf("want 'no published release' error, got %v", err)
	}
}

func TestDownloadBinary_OK(t *testing.T) {
	up, _, _ := newFakeRelease(t, "v0.11.2", "the-real-binary")
	dst := t.TempDir()
	binPath, err := up.DownloadBinary(context.Background(), "v0.11.2", runtime.GOOS, runtime.GOARCH, dst)
	if err != nil {
		t.Fatalf("DownloadBinary: %v", err)
	}
	got, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatalf("read extracted binary: %v", err)
	}
	if string(got) != "the-real-binary" {
		t.Errorf("extracted contents = %q, want %q", got, "the-real-binary")
	}
}

func TestDownloadBinary_ChecksumMismatch(t *testing.T) {
	asset, err := AssetName(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skipf("unsupported test platform: %v", err)
	}
	archive := makeTarGz(t, "real")
	tag := "v0.11.2"
	mux := http.NewServeMux()
	mux.HandleFunc("/"+Repo+"/releases/download/"+tag+"/"+asset, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(archive)
	})
	// checksums.txt advertises a hash for a DIFFERENT payload → must be rejected.
	mux.HandleFunc("/"+Repo+"/releases/download/"+tag+"/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", sha256hex([]byte("tampered")), asset)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	up := &Updater{HTTP: srv.Client(), APIBase: srv.URL, DownloadBase: srv.URL, Repo: Repo}

	_, err = up.DownloadBinary(context.Background(), tag, runtime.GOOS, runtime.GOARCH, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("want checksum mismatch error, got %v", err)
	}
}

// makeZip builds a .zip containing a single file with the given name+contents,
// matching how GoReleaser packages the Windows build (qvr.exe inside a zip).
func makeZip(t *testing.T, name, contents string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatalf("zip create: %v", err)
	}
	if _, err := w.Write([]byte(contents)); err != nil {
		t.Fatalf("zip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

// TestDownloadBinary_Zip exercises the Windows path (qvr_Windows_x86_64.zip with
// qvr.exe inside) on any host — extraction is pure Go, so it needs no real
// Windows runner.
func TestDownloadBinary_Zip(t *testing.T) {
	const tag = "v0.11.2"
	asset, err := AssetName("windows", "amd64")
	if err != nil {
		t.Fatalf("AssetName: %v", err)
	}
	archive := makeZip(t, "qvr.exe", "WINDOWS-BINARY")
	checksums := fmt.Sprintf("%s  %s\n", sha256hex(archive), asset)

	mux := http.NewServeMux()
	mux.HandleFunc("/"+Repo+"/releases/download/"+tag+"/"+asset, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(archive)
	})
	mux.HandleFunc("/"+Repo+"/releases/download/"+tag+"/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(checksums))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	up := &Updater{HTTP: srv.Client(), APIBase: srv.URL, DownloadBase: srv.URL, Repo: Repo}

	binPath, err := up.DownloadBinary(context.Background(), tag, "windows", "amd64", t.TempDir())
	if err != nil {
		t.Fatalf("DownloadBinary (zip): %v", err)
	}
	got, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatalf("read extracted binary: %v", err)
	}
	if string(got) != "WINDOWS-BINARY" {
		t.Errorf("extracted contents = %q, want WINDOWS-BINARY", got)
	}
}

func TestReplace_AtomicSwap(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "qvr")
	if err := os.WriteFile(target, []byte("OLD"), 0o755); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	newBin := filepath.Join(dir, "staged")
	if err := os.WriteFile(newBin, []byte("NEW"), 0o755); err != nil {
		t.Fatalf("seed new: %v", err)
	}

	if err := Replace(target, newBin); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(got) != "NEW" {
		t.Errorf("target contents = %q, want NEW", got)
	}
	// No leftover temp files in the directory.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".qvr.tmp.") {
			t.Errorf("leftover temp file after Replace: %s", e.Name())
		}
	}
}
