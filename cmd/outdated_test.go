package cmd

import (
	"errors"
	"testing"

	"github.com/raks097/quiver/internal/config"
	"github.com/raks097/quiver/internal/git"
	"github.com/raks097/quiver/internal/model"
)

func newRefs(m map[string]string) *git.RemoteRefInfo {
	return &git.RemoteRefInfo{Refs: m}
}

func TestComputeOutdated_UpToDate(t *testing.T) {
	entry := &model.LockEntry{
		Name: "demo", Registry: "acme", Ref: "main",
		Commit: "aaaaaaa1111111111111111111111111111111111",
	}
	remote := remoteResult{refs: newRefs(map[string]string{
		"refs/heads/main": "aaaaaaa1111111111111111111111111111111111",
	})}
	got := computeOutdated(entry, remote)
	if got.State != outStateUpToDate {
		t.Errorf("state = %q, want %q", got.State, outStateUpToDate)
	}
}

func TestComputeOutdated_Behind(t *testing.T) {
	entry := &model.LockEntry{
		Name: "demo", Registry: "acme", Ref: "main",
		Commit: "0000000000000000000000000000000000000000",
	}
	remote := remoteResult{refs: newRefs(map[string]string{
		"refs/heads/main": "ffffffffffffffffffffffffffffffffffffffff",
	})}
	got := computeOutdated(entry, remote)
	if got.State != outStateBehind {
		t.Errorf("state = %q, want %q", got.State, outStateBehind)
	}
	if got.Remote != "ffffffffffffffffffffffffffffffffffffffff" {
		t.Errorf("remote hash not surfaced: %q", got.Remote)
	}
}

func TestComputeOutdated_TagPinned(t *testing.T) {
	entry := &model.LockEntry{
		Name: "demo", Registry: "acme", Ref: "v1.2.0",
		Commit: "abcabcabcabcabcabcabcabcabcabcabcabcabca",
	}
	remote := remoteResult{refs: newRefs(map[string]string{
		"refs/heads/main":  "ddddddddddddddddddddddddddddddddddddddd0",
		"refs/tags/v1.2.0": "abcabcabcabcabcabcabcabcabcabcabcabcabca",
	})}
	got := computeOutdated(entry, remote)
	if got.State != outStateUpToDate {
		t.Errorf("tag-pinned skill should be up-to-date, got %q", got.State)
	}
}

func TestComputeOutdated_TagPinned_NewerTagAvailable(t *testing.T) {
	entry := &model.LockEntry{
		Name: "demo", Registry: "acme", Ref: "v0.1.1",
		Commit: "abcabcabcabcabcabcabcabcabcabcabcabcabca",
	}
	remote := remoteResult{refs: newRefs(map[string]string{
		"refs/heads/main":  "ddddddddddddddddddddddddddddddddddddddd0",
		"refs/tags/v0.1.1": "abcabcabcabcabcabcabcabcabcabcabcabcabca",
		"refs/tags/v0.2.0": "1111111111111111111111111111111111111111",
	})}
	got := computeOutdated(entry, remote)
	if got.State != outStateBehind {
		t.Fatalf("state = %q, want behind", got.State)
	}
	if got.LatestTag != "v0.2.0" {
		t.Errorf("LatestTag = %q, want v0.2.0", got.LatestTag)
	}
	if got.Remote != "1111111111111111111111111111111111111111" {
		t.Errorf("Remote = %q, want the v0.2.0 commit", got.Remote)
	}
}

func TestComputeOutdated_TagPinned_PeeledTagRefsIgnored(t *testing.T) {
	entry := &model.LockEntry{
		Name: "demo", Registry: "acme", Ref: "v1.0.0",
		Commit: "abcabcabcabcabcabcabcabcabcabcabcabcabca",
	}
	// Some servers publish `v1.0.0^{}` peeled refs. They must not win the
	// "latest tag" election over the semver-named ref.
	remote := remoteResult{refs: newRefs(map[string]string{
		"refs/tags/v1.0.0":    "abcabcabcabcabcabcabcabcabcabcabcabcabca",
		"refs/tags/v1.0.0^{}": "abcabcabcabcabcabcabcabcabcabcabcabcabca",
	})}
	got := computeOutdated(entry, remote)
	if got.State != outStateUpToDate {
		t.Fatalf("state = %q, want up-to-date", got.State)
	}
}

func TestComputeOutdated_RefNotOnRemote(t *testing.T) {
	entry := &model.LockEntry{
		Name: "demo", Registry: "acme", Ref: "feature/x",
		Commit: "1111111111111111111111111111111111111111",
	}
	remote := remoteResult{refs: newRefs(map[string]string{
		"refs/heads/main": "ffffffffffffffffffffffffffffffffffffffff",
	})}
	got := computeOutdated(entry, remote)
	if got.State != outStateUnreachable {
		t.Errorf("missing branch should be unreachable, got %q", got.State)
	}
	if got.Reason == "" {
		t.Error("expected a reason explaining the missing ref")
	}
}

func TestComputeOutdated_Unreachable(t *testing.T) {
	entry := &model.LockEntry{
		Name: "demo", Registry: "acme", Ref: "main",
		Commit: "1111111111111111111111111111111111111111",
	}
	remote := remoteResult{err: errors.New("network: connection refused")}
	got := computeOutdated(entry, remote)
	if got.State != outStateUnreachable {
		t.Errorf("network error should give unreachable, got %q", got.State)
	}
	if got.Reason == "" {
		t.Error("expected a reason for unreachable")
	}
}

func TestComputeOutdated_LinkInstall(t *testing.T) {
	entry := &model.LockEntry{
		Name: "demo", Source: "/local/path", Ref: "local",
	}
	got := computeOutdated(entry, remoteResult{})
	if got.State != outStateLink {
		t.Errorf("link install should report link state, got %q", got.State)
	}
}

// TestRemoteURLFor pins the resolution rules used by qvr outdated:
//   - "registry" entries look the URL up in cfg.Registries
//   - "subdir" entries (qvr add) read RepoURL off the lock entry directly
//   - both fail with a clear error when the source isn't usable
func TestRemoteURLFor_RegistrySource(t *testing.T) {
	cfg := config.Default()
	cfg.Registries["acme"] = config.RegistryConfig{URL: "https://example.com/acme.git"}
	url, err := remoteURLFor(&model.LockEntry{Registry: "acme"}, cfg)
	if err != nil || url != "https://example.com/acme.git" {
		t.Errorf("registry URL: got (%q, %v)", url, err)
	}
}

func TestRemoteURLFor_RegistrySourceMissing(t *testing.T) {
	cfg := config.Default()
	if _, err := remoteURLFor(&model.LockEntry{Registry: "ghost"}, cfg); err == nil {
		t.Error("expected error for missing registry, got nil")
	}
}

// v5: source carries the fetch URL directly, so remoteURLFor returns it
// regardless of whether the entry has a configured registry name.
func TestRemoteURLFor_UsesSourceURL(t *testing.T) {
	cfg := config.Default()
	url, err := remoteURLFor(&model.LockEntry{
		Registry: "github.com--acme--skills",
		Source:   "https://github.com/acme/skills.git",
	}, cfg)
	if err != nil || url != "https://github.com/acme/skills.git" {
		t.Errorf("source URL: got (%q, %v)", url, err)
	}
}

func TestRemoteURLFor_LinkEntryErrors(t *testing.T) {
	cfg := config.Default()
	_, err := remoteURLFor(&model.LockEntry{
		Name:   "demo",
		Source: "/local/path",
		Ref:    "local",
	}, cfg)
	if err == nil {
		t.Error("link install should error from remoteURLFor")
	}
}
