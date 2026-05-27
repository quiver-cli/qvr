package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/raks097/quiver/internal/config"
	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/ops"
	"github.com/raks097/quiver/internal/ops/store"
)

// isolatedHome returns a fresh $QUIVER_HOME for a test. Sets up:
//   - QUIVER_HOME env var
//   - config.yaml with ops.enabled=true (or false when dis=true)
//   - qvr.lock.json with a "foo" skill entry rooted at a real dir
//
// Returns the skill's worktree dir (for crafting event paths) plus a
// function to read back events from the SQLite store.
func isolatedHome(t *testing.T, opsEnabled bool) (worktree string, readEvents func() []*ops.Event) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)

	worktree = filepath.Join(home, "worktrees", "foo")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write lockfile with one entry pointing at the worktree.
	lf := &model.LockFile{
		Version: model.LockFileVersion,
		Skills: map[string]*model.LockEntry{
			"foo": {
				Name: "foo", Registry: "team", Commit: "abc123",
				Worktree:    worktree,
				InstalledAt: time.Now(),
				UpdatedAt:   time.Now(),
			},
		},
	}
	buf, _ := json.Marshal(lf)
	if err := os.WriteFile(filepath.Join(home, model.LockFileName), buf, 0o644); err != nil {
		t.Fatal(err)
	}

	// Write config.
	cfg := &config.Config{}
	cfg.Ops.Enabled = opsEnabled
	cfgBuf, _ := os.ReadFile(config.Path())
	_ = cfgBuf
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}

	readEvents = func() []*ops.Event {
		s, err := store.Open(context.Background(), store.OpenOptions{Path: ops.DBPath(cfg)})
		if err != nil {
			t.Fatalf("open store: %v", err)
		}
		defer s.Close()
		events, err := s.QueryEvents(context.Background(), &store.EventFilter{})
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		return events
	}

	return worktree, readEvents
}

// runHookCmd invokes `qvr _hook <agent> <hookType>` with stdin. It
// drives the Cobra command directly so tests don't shell out.
func runHookCmd(t *testing.T, stdin []byte, args ...string) (string, string, error) {
	t.Helper()
	// Cobra holds global state; reset the command tree for every call.
	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetIn(bytes.NewReader(stdin))
	rootCmd.SetArgs(append([]string{"_hook"}, args...))
	t.Cleanup(func() {
		rootCmd.SetIn(os.Stdin)
		rootCmd.SetOut(io.Discard)
		rootCmd.SetErr(io.Discard)
	})
	err := rootCmd.Execute()
	return stdout.String(), stderr.String(), err
}

// --- Tests ---

func TestHook_DisabledOps_NoOp(t *testing.T) {
	worktree, readEvents := isolatedHome(t, false)
	event := map[string]any{
		"agent_name":       "claude",
		"agent_session_id": "s",
		"action_type":      "file_read",
		"payload":          map[string]any{"path": filepath.Join(worktree, "SKILL.md")},
	}
	raw, _ := json.Marshal(event)
	_, _, err := runHookCmd(t, raw, "generic", "PostToolUse")
	if err != nil {
		t.Errorf("disabled hook should be silent no-op; got err %v", err)
	}
	// No DB created.
	home := os.Getenv("QUIVER_HOME")
	if _, err := os.Stat(filepath.Join(home, "skillops.db")); !os.IsNotExist(err) {
		t.Errorf("DB should not be created when ops disabled; got err %v", err)
	}
	_ = readEvents // would fail if we called it — no DB
}

func TestHook_Happy_PersistsEvent(t *testing.T) {
	worktree, readEvents := isolatedHome(t, true)
	event := map[string]any{
		"agent_name":       "claude",
		"agent_session_id": "sess-abc",
		"action_type":      "file_read",
		"payload":          map[string]any{"path": filepath.Join(worktree, "SKILL.md")},
	}
	raw, _ := json.Marshal(event)
	stdout, stderr, err := runHookCmd(t, raw, "generic", "PostToolUse")
	if err != nil {
		t.Fatalf("hook: err=%v stdout=%q stderr=%q", err, stdout, stderr)
	}

	events := readEvents()
	if len(events) != 1 {
		t.Fatalf("expected 1 event; got %d", len(events))
	}
	e := events[0]
	if e.SkillName != "foo" {
		t.Errorf("SkillName=%q want foo", e.SkillName)
	}
	if e.AgentName != "claude" {
		t.Errorf("AgentName=%q want claude", e.AgentName)
	}
	if e.ActionType != ops.ActionFileRead {
		t.Errorf("ActionType=%q want file_read", e.ActionType)
	}
}

func TestHook_UnknownAgent_DropsNotErrors(t *testing.T) {
	worktree, _ := isolatedHome(t, true)
	raw, _ := json.Marshal(map[string]any{
		"agent_name":       "nobody",
		"agent_session_id": "s",
		"action_type":      "file_read",
		"payload":          map[string]any{"path": filepath.Join(worktree, "x")},
	})
	_, stderr, err := runHookCmd(t, raw, "unknown-adapter", "PostToolUse")
	// Hook pipeline must not fail the caller; it only emits to stderr.
	if err != nil {
		t.Errorf("expected nil error for unknown adapter; got %v", err)
	}
	if !strings.Contains(stderr, "unknown") {
		t.Errorf("expected stderr to mention unknown; got %q", stderr)
	}
}

func TestHook_InvalidJSON_RecordsHookError(t *testing.T) {
	_, readEvents := isolatedHome(t, true)
	_, _, err := runHookCmd(t, []byte("not json"), "generic", "PostToolUse")
	if err != nil {
		t.Errorf("expected nil error on bad JSON; got %v", err)
	}
	// No event should be recorded, but a self_audit row should exist.
	if got := readEvents(); len(got) != 0 {
		t.Errorf("expected 0 events; got %d", len(got))
	}
	// Open store directly to check self_audits via stats.
	s, err := store.Open(context.Background(), store.OpenOptions{Path: filepath.Join(os.Getenv("QUIVER_HOME"), "skillops.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	stats, _ := s.Stats(context.Background())
	if stats.SelfAuditCount != 1 {
		t.Errorf("expected 1 self_audit row; got %d", stats.SelfAuditCount)
	}
}

func TestHook_UnattributedPath_DropsNotErrors(t *testing.T) {
	_, readEvents := isolatedHome(t, true)
	raw, _ := json.Marshal(map[string]any{
		"agent_name":       "claude",
		"agent_session_id": "s",
		"action_type":      "file_read",
		"payload":          map[string]any{"path": "/entirely/unrelated/path.md"},
	})
	if _, _, err := runHookCmd(t, raw, "generic", "PostToolUse"); err != nil {
		t.Errorf("unexpected err: %v", err)
	}
	if got := readEvents(); len(got) != 0 {
		t.Errorf("expected 0 events (unattributed); got %d", len(got))
	}
}

func TestHook_EmptyStdin_NoOp(t *testing.T) {
	_, readEvents := isolatedHome(t, true)
	if _, _, err := runHookCmd(t, []byte(""), "generic", "PostToolUse"); err != nil {
		t.Errorf("expected nil error for empty stdin; got %v", err)
	}
	if got := readEvents(); len(got) != 0 {
		t.Errorf("expected 0 events for empty stdin; got %d", len(got))
	}
}

func TestHook_HookCommandIsHidden(t *testing.T) {
	var found bool
	for _, c := range rootCmd.Commands() {
		if c.Use == "_hook <agent> <hook_type>" {
			found = true
			if !c.Hidden {
				t.Errorf("_hook command should be Hidden")
			}
		}
	}
	if !found {
		t.Errorf("_hook command not registered")
	}
}

func TestHook_SensitivePathStripsContent_EndToEnd(t *testing.T) {
	worktree, readEvents := isolatedHome(t, true)
	envPath := filepath.Join(worktree, ".env")
	raw, _ := json.Marshal(map[string]any{
		"agent_name":       "claude",
		"agent_session_id": "s",
		"action_type":      "file_write",
		"diff_content":     "AWS_SECRET_ACCESS_KEY=realsecret",
		"payload": map[string]any{
			"path":       envPath,
			"new_string": "AWS_SECRET_ACCESS_KEY=realsecret",
		},
	})
	if _, _, err := runHookCmd(t, raw, "generic", "PostToolUse"); err != nil {
		t.Fatal(err)
	}
	events := readEvents()
	if len(events) != 1 {
		t.Fatalf("expected 1 event; got %d", len(events))
	}
	e := events[0]
	if !e.IsSensitive {
		t.Errorf("expected IsSensitive=true")
	}
	if strings.Contains(string(e.Payload), "realsecret") {
		t.Errorf("secret survived: %s", e.Payload)
	}
	if e.DiffContent != "" {
		t.Errorf("expected DiffContent stripped; got %q", e.DiffContent)
	}
}

// --- Adapter bridge smoke ---

func TestStoreSessionAdapter_BridgesAllMethods(t *testing.T) {
	// Bridge compiles and all four methods pass through.
	home := t.TempDir()
	s, err := store.Open(context.Background(), store.OpenOptions{Path: filepath.Join(home, "x.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a := storeSessionAdapter{s}

	sess := ops.NewSession("claude", "sess", time.Now())
	if err := a.UpsertSession(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	if got, err := a.GetSession(context.Background(), sess.ID); err != nil {
		t.Fatal(err)
	} else if got == nil {
		t.Errorf("session not found")
	}
	e := &ops.Event{
		ID:           uuid.New(),
		SessionID:    sess.ID,
		AgentName:    "claude",
		SkillName:    "foo",
		ActionType:   ops.ActionFileRead,
		ResultStatus: ops.ResultSuccess,
		Timestamp:    time.Now(),
	}
	if err := a.SaveEvent(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	err = a.AppendSelfAudit(context.Background(), &ops.SelfAuditEntry{
		Action:   ops.SelfAuditHookError,
		Result:   ops.SelfAuditResultError,
		ErrorMsg: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestStoreSessionAdapter_NilSelfAuditRejected(t *testing.T) {
	home := t.TempDir()
	s, err := store.Open(context.Background(), store.OpenOptions{Path: filepath.Join(home, "x.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a := storeSessionAdapter{s}
	if err := a.AppendSelfAudit(context.Background(), nil); err == nil {
		t.Errorf("expected rejection")
	}
}
