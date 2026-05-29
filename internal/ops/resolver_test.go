package ops

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/registry"
)

// fixture builds a fake lockfile at $tmp/qvr.lock.json with entries
// whose worktrees are real directories on disk. Returns the lockfile
// path and a skill-name → worktree-dir map so tests can craft events
// with plausible paths.
type fixture struct {
	root      string
	lockPath  string
	worktrees map[string]string
}

func newFixture(t *testing.T, entries ...*model.LockEntry) *fixture {
	t.Helper()
	root := t.TempDir()
	// Pin QUIVER_HOME so registry.WorktreePath resolves inside root and
	// each test entry's derived worktree lives in the fixture's tempdir.
	t.Setenv("QUIVER_HOME", root)
	wt := map[string]string{}

	for _, e := range entries {
		var dir string
		switch {
		case e.IsLink():
			dir = e.Source
		case strings.HasPrefix(e.Source, "/"):
			// Caller already wired Source as a local-link path.
			dir = e.Source
		default:
			// Fill in v5 fields so EntryWorktreePath resolves under
			// QUIVER_HOME. Tests don't generally care about specific
			// registry/commit values, just that the worktree path is
			// stable and on disk.
			if e.Registry == "" {
				e.Registry = "test"
			}
			if e.Commit == "" {
				e.Commit = "abc1234"
			}
			if e.Ref == "" {
				e.Ref = "main"
			}
			if e.Source == "" {
				e.Source = "git@example.test:" + e.Registry + ".git"
			}
			dir = registry.WorktreePath(e.Registry, e.Name, registry.ShortSHA(e.Commit))
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		wt[e.Name] = dir
		if e.InstalledAt.IsZero() {
			e.InstalledAt = time.Now()
		}
	}

	lf := &model.LockFile{Version: model.LockFileVersion, Skills: map[string]*model.LockEntry{}}
	for _, e := range entries {
		lf.Skills[e.Name] = e
	}
	buf, err := json.MarshalIndent(lf, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, model.LockFileName)
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}

	return &fixture{root: root, lockPath: path, worktrees: wt}
}

// event creates a mock event with the given path in its payload.
func (f *fixture) event(path string, action ActionType) *Event {
	e := &Event{
		ID:           uuid.New(),
		SessionID:    uuid.New(),
		AgentName:    "claude",
		ActionType:   action,
		ResultStatus: ResultSuccess,
		Timestamp:    time.Now(),
	}
	_ = e.SetPayload(FileReadPayload{Path: path})
	return e
}

// --- Basic attribution ---

func TestResolver_NoLockfileNoAttribution(t *testing.T) {
	r, err := NewResolver("/does/not/exist")
	if err != nil {
		t.Fatal(err)
	}
	_, ok := r.Attribute(&Event{})
	if ok {
		t.Errorf("expected no attribution from missing lockfile")
	}
}

func TestResolver_EmptyLockfileNoAttribution(t *testing.T) {
	f := newFixture(t)
	r, _ := NewResolver(f.lockPath)
	_, ok := r.Attribute(f.event("/a/b/c", ActionFileRead))
	if ok {
		t.Errorf("expected no attribution from empty lockfile")
	}
}

func TestResolver_AttributesRegistryInstall(t *testing.T) {
	f := newFixture(t, &model.LockEntry{
		Name: "foo", Registry: "team", Commit: "abc123",
	})
	r, _ := NewResolver(f.lockPath)

	e := f.event(filepath.Join(f.worktrees["foo"], "SKILL.md"), ActionFileRead)
	attr, ok := r.Attribute(e)
	if !ok {
		t.Fatalf("expected attribution")
	}
	if attr.Name != "foo" {
		t.Errorf("Name=%q want foo", attr.Name)
	}
	if attr.Registry != "team" {
		t.Errorf("Registry=%q want team", attr.Registry)
	}
	if attr.Commit != "abc123" {
		t.Errorf("Commit=%q want abc123", attr.Commit)
	}
	if attr.RelPath != "SKILL.md" {
		t.Errorf("RelPath=%q want SKILL.md", attr.RelPath)
	}
}

func TestResolver_AttributesLinkedSkill(t *testing.T) {
	// Link installs in v5: Source holds an absolute local path.
	tmp := t.TempDir()
	linkedDir := filepath.Join(tmp, "my-linked-skill")
	if err := os.MkdirAll(linkedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	f := newFixture(t, &model.LockEntry{
		Name:   "linked",
		Source: linkedDir,
		Ref:    "local",
	})
	r, _ := NewResolver(f.lockPath)
	e := f.event(filepath.Join(f.worktrees["linked"], "subdir", "file.md"), ActionFileRead)
	attr, ok := r.Attribute(e)
	if !ok {
		t.Fatalf("expected attribution for linked skill")
	}
	if attr.Name != "linked" {
		t.Errorf("Name=%q want linked", attr.Name)
	}
}

func TestResolver_RejectsPathOutsideSkills(t *testing.T) {
	f := newFixture(t, &model.LockEntry{Name: "foo"})
	r, _ := NewResolver(f.lockPath)
	e := f.event("/totally/unrelated/path.md", ActionFileRead)
	_, ok := r.Attribute(e)
	if ok {
		t.Errorf("expected no attribution for outside path")
	}
}

func TestResolver_SkipsDisabledEntries(t *testing.T) {
	f := newFixture(t, &model.LockEntry{
		Name:     "disabled",
		Disabled: true,
	})
	r, _ := NewResolver(f.lockPath)
	e := f.event(filepath.Join(f.worktrees["disabled"], "f.md"), ActionFileRead)
	_, ok := r.Attribute(e)
	if ok {
		t.Errorf("expected disabled entries to be excluded")
	}
}

// --- Path normalisation ---

func TestResolver_HandlesRelativePath(t *testing.T) {
	f := newFixture(t, &model.LockEntry{Name: "foo"})
	r, _ := NewResolver(f.lockPath)

	// Chdir so a relative path resolves under the worktree.
	prev, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(prev) })
	if err := os.Chdir(f.worktrees["foo"]); err != nil {
		t.Fatal(err)
	}

	e := f.event("./SKILL.md", ActionFileRead)
	_, ok := r.Attribute(e)
	if !ok {
		t.Errorf("expected relative path to resolve")
	}
}

func TestResolver_NestedSkillsMostSpecificWins(t *testing.T) {
	// Outer skill has a worktree that contains the inner skill's
	// worktree. The inner (more-specific) attribution should win.
	// v5: entries must be link installs (Ref=local) so the resolver's
	// EntryWorktreePath honours the Source override rather than computing
	// from registry+commit.
	root := t.TempDir()
	outerDir := filepath.Join(root, "worktrees", "outer")
	innerDir := filepath.Join(outerDir, "nested", "inner")
	if err := os.MkdirAll(innerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lf := &model.LockFile{Version: model.LockFileVersion, Skills: map[string]*model.LockEntry{
		"outer": {Name: "outer", Source: outerDir, Ref: "local"},
		"inner": {Name: "inner", Source: innerDir, Ref: "local"},
	}}
	buf, _ := json.Marshal(lf)
	lockPath := filepath.Join(root, model.LockFileName)
	_ = os.WriteFile(lockPath, buf, 0o644)

	r, _ := NewResolver(lockPath)
	e := &Event{
		SessionID:  uuid.New(),
		AgentName:  "claude",
		ActionType: ActionFileRead,
	}
	_ = e.SetPayload(FileReadPayload{Path: filepath.Join(innerDir, "SKILL.md")})

	attr, ok := r.Attribute(e)
	if !ok || attr.Name != "inner" {
		t.Errorf("expected inner skill to win; got name=%q", attr.Name)
	}
}

// --- Symlink deref ---

func TestResolver_FollowsSymlinkedPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require admin on Windows")
	}
	root := t.TempDir()
	realDir := filepath.Join(root, "real-worktree")
	link := filepath.Join(root, "link-to-worktree")
	_ = os.MkdirAll(realDir, 0o755)
	if err := os.Symlink(realDir, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	// Link install: Source carries the symlink target so the resolver's
	// EntryWorktreePath returns realDir (after the resolver's own
	// EvalSymlinks pass).
	lf := &model.LockFile{Version: model.LockFileVersion, Skills: map[string]*model.LockEntry{
		"foo": {Name: "foo", Source: realDir, Ref: "local"},
	}}
	buf, _ := json.Marshal(lf)
	lockPath := filepath.Join(root, model.LockFileName)
	_ = os.WriteFile(lockPath, buf, 0o644)

	r, _ := NewResolver(lockPath)
	e := &Event{SessionID: uuid.New(), ActionType: ActionFileRead, AgentName: "claude"}
	// Event references the symlink path; resolver must follow.
	_ = e.SetPayload(FileReadPayload{Path: filepath.Join(link, "SKILL.md")})
	_, ok := r.Attribute(e)
	if !ok {
		t.Errorf("expected attribution via symlinked path")
	}
}

// --- Lockfile merge ---

func TestResolver_MergesMultipleLockfiles(t *testing.T) {
	// Global and local lockfiles: both have entries, local overrides
	// global on name collision.
	f1 := newFixture(t, &model.LockEntry{Name: "foo", Registry: "global"})
	f2 := newFixture(t, &model.LockEntry{Name: "foo", Registry: "local"})

	r, _ := NewResolver(f1.lockPath, f2.lockPath)
	e := &Event{SessionID: uuid.New(), ActionType: ActionFileRead, AgentName: "claude"}
	_ = e.SetPayload(FileReadPayload{Path: filepath.Join(f2.worktrees["foo"], "SKILL.md")})

	attr, ok := r.Attribute(e)
	if !ok {
		t.Fatalf("expected attribution")
	}
	if attr.Registry != "local" {
		t.Errorf("expected local to shadow global; got %q", attr.Registry)
	}
}

func TestResolver_IgnoresEmptyLockfilePath(t *testing.T) {
	f := newFixture(t, &model.LockEntry{Name: "foo"})
	r, err := NewResolver("", f.lockPath, "")
	if err != nil {
		t.Fatal(err)
	}
	e := f.event(filepath.Join(f.worktrees["foo"], "x"), ActionFileRead)
	if _, ok := r.Attribute(e); !ok {
		t.Errorf("expected attribution from non-empty path")
	}
}

// --- Session fallback ---

func TestResolver_SessionFallbackAttributesPathlessEvent(t *testing.T) {
	f := newFixture(t, &model.LockEntry{Name: "foo"})
	r, _ := NewResolver(f.lockPath)

	// First event: has a path → attributes + caches.
	sessID := uuid.New()
	e1 := &Event{SessionID: sessID, AgentName: "claude", ActionType: ActionFileRead}
	_ = e1.SetPayload(FileReadPayload{Path: filepath.Join(f.worktrees["foo"], "SKILL.md")})
	if _, ok := r.Attribute(e1); !ok {
		t.Fatal("first event should attribute")
	}

	// Second event: pathless (session_end, no payload) → inherits.
	e2 := &Event{SessionID: sessID, AgentName: "claude", ActionType: ActionSessionEnd}
	attr, ok := r.Attribute(e2)
	if !ok {
		t.Errorf("expected session-fallback attribution")
	}
	if attr.Name != "foo" {
		t.Errorf("fallback Name=%q want foo", attr.Name)
	}
}

func TestResolver_SessionFallbackIsolatedPerSession(t *testing.T) {
	f := newFixture(t, &model.LockEntry{Name: "foo"})
	r, _ := NewResolver(f.lockPath)

	// Session A → attributes foo.
	a := uuid.New()
	e := &Event{SessionID: a, AgentName: "claude", ActionType: ActionFileRead}
	_ = e.SetPayload(FileReadPayload{Path: filepath.Join(f.worktrees["foo"], "x")})
	_, _ = r.Attribute(e)

	// Session B → fresh; no fallback available.
	b := uuid.New()
	pathless := &Event{SessionID: b, AgentName: "claude", ActionType: ActionSessionEnd}
	_, ok := r.Attribute(pathless)
	if ok {
		t.Errorf("expected no fallback for different session")
	}
}

func TestResolver_SessionFallbackBoundedByCap(t *testing.T) {
	f := newFixture(t, &model.LockEntry{Name: "foo"})
	r, _ := NewResolver(f.lockPath)

	// Register 1500 session attributions. The bound is 1024, so some
	// entries will be evicted — but the map size must never exceed 1024.
	lr, ok := r.(*lockResolver)
	if !ok {
		t.Fatalf("NewResolver returned %T, want *lockResolver", r)
	}
	for i := 0; i < 1500; i++ {
		e := &Event{SessionID: uuid.New(), AgentName: "claude", ActionType: ActionFileRead}
		_ = e.SetPayload(FileReadPayload{Path: filepath.Join(f.worktrees["foo"], "x")})
		_, _ = r.Attribute(e)
	}
	lr.sessionMu.RLock()
	size := len(lr.sessionLast)
	lr.sessionMu.RUnlock()
	if size > 1024 {
		t.Errorf("session cache grew unbounded: %d entries", size)
	}
}

// --- Working-directory fallback ---

func TestResolver_UsesWorkingDirectory(t *testing.T) {
	f := newFixture(t, &model.LockEntry{Name: "foo"})
	r, _ := NewResolver(f.lockPath)

	// Event has no payload path but WorkingDirectory sits inside foo's
	// worktree — resolver should still attribute.
	e := &Event{
		SessionID:        uuid.New(),
		AgentName:        "claude",
		ActionType:       ActionSessionStart,
		WorkingDirectory: f.worktrees["foo"],
	}
	_, ok := r.Attribute(e)
	if !ok {
		t.Errorf("expected WorkingDirectory fallback to attribute")
	}
}

// --- Caching + concurrency ---

func TestResolver_ConcurrentAttribute(t *testing.T) {
	f := newFixture(t, &model.LockEntry{Name: "foo"})
	r, _ := NewResolver(f.lockPath)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e := &Event{SessionID: uuid.New(), AgentName: "claude", ActionType: ActionFileRead}
			_ = e.SetPayload(FileReadPayload{Path: filepath.Join(f.worktrees["foo"], "x")})
			_, _ = r.Attribute(e)
		}()
	}
	wg.Wait()
}

// --- ensureFields helper ---

func TestEnsureFields_CopiesAttribution(t *testing.T) {
	e := &Event{}
	ensureFields(e, Attribution{Name: "x", Registry: "r", Commit: "c", RelPath: "p"})
	if e.SkillName != "x" {
		t.Errorf("SkillName=%q", e.SkillName)
	}
	if e.SkillRegistry != "r" {
		t.Errorf("SkillRegistry=%q", e.SkillRegistry)
	}
	if e.SkillCommit != "c" {
		t.Errorf("SkillCommit=%q", e.SkillCommit)
	}
	if e.SkillPath != "p" {
		t.Errorf("SkillPath=%q", e.SkillPath)
	}
}

func TestEnsureFields_NilSafe(t *testing.T) {
	// Must not panic.
	ensureFields(nil, Attribution{Name: "x"})
}

// --- Internal helper tests ---

func TestDescends_TrueCases(t *testing.T) {
	cases := [][2]string{
		{"/a/b/c", "/a/b"},
		{"/a/b", "/a/b"},
		{"/a/b/c/d/e", "/a/b"},
	}
	for _, c := range cases {
		if !descends(c[0], c[1]) {
			t.Errorf("descends(%q, %q) = false; want true", c[0], c[1])
		}
	}
}

func TestDescends_FalseCases(t *testing.T) {
	cases := [][2]string{
		{"/a/bc", "/a/b"}, // prefix but not segment
		{"/a", "/a/b"},    // above
		{"/x", "/a"},      // unrelated
		{"", "/a"},        // empty
		{"/a", ""},        // empty root
	}
	for _, c := range cases {
		if descends(c[0], c[1]) {
			t.Errorf("descends(%q, %q) = true; want false", c[0], c[1])
		}
	}
}

func TestLockfileExists(t *testing.T) {
	if lockfileExists("") {
		t.Errorf("empty path should not exist")
	}
	if lockfileExists("/does/not/exist/xyz") {
		t.Errorf("non-existent path should return false")
	}
	tmp := t.TempDir()
	p := filepath.Join(tmp, "x")
	_ = os.WriteFile(p, []byte(""), 0o644)
	if !lockfileExists(p) {
		t.Errorf("existing file should return true")
	}
	if lockfileExists(tmp) {
		t.Errorf("directory should return false")
	}
}
