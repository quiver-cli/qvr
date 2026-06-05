package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/quiver-cli/qvr/internal/config"
	"github.com/quiver-cli/qvr/internal/ops"
	"github.com/quiver-cli/qvr/internal/ops/store"
)

// isolatedHome returns a fresh $QUIVER_HOME with config (ops.enabled per the
// flag) plus a reader over the captured raw_traces.
func isolatedHome(t *testing.T, opsEnabled bool) (home string, readRaw func() []*ops.RawTrace) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("QUIVER_HOME", home)

	cfg := &config.Config{}
	cfg.Ops.Enabled = opsEnabled
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}

	readRaw = func() []*ops.RawTrace {
		s, err := store.Open(context.Background(), store.OpenOptions{Path: ops.DBPath(cfg)})
		if err != nil {
			t.Fatalf("open store: %v", err)
		}
		defer s.Close()
		rows, err := s.QueryRawTraces(context.Background(), &store.RawTraceFilter{})
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		return rows
	}
	return home, readRaw
}

// writeTranscript drops a Claude-style transcript JSONL and returns its path +
// session id. The assistant turn loads a skill (a "Skill" tool-call) so the
// captured session is skill-attributed and survives the skill-only retention
// gate — the common case these audit tests exercise.
func writeTranscript(t *testing.T, dir string) (path, sessionID string) {
	t.Helper()
	sessionID = "550e8400-e29b-41d4-a716-446655440000"
	path = filepath.Join(dir, "session.jsonl")
	lines := []string{
		`{"type":"user","timestamp":"2026-06-02T00:00:00.000Z","message":{"role":"user","content":"do the thing"}}`,
		`{"type":"assistant","timestamp":"2026-06-02T00:00:01.000Z","message":{"role":"assistant","model":"claude-opus-4-8","usage":{"input_tokens":10,"output_tokens":3},"content":[{"type":"tool_use","id":"toolu_skill","name":"Skill","input":{"command":"code-review"}},{"type":"text","text":"done"}]}}`,
	}
	if err := os.WriteFile(path, []byte(joinLines(lines)), 0o644); err != nil {
		t.Fatal(err)
	}
	return path, sessionID
}

func joinLines(lines []string) string {
	out := ""
	for _, l := range lines {
		out += l + "\n"
	}
	return out
}

func payloadFor(sessionID, transcript string) []byte {
	b, _ := json.Marshal(map[string]string{
		"session_id":      sessionID,
		"transcript_path": transcript,
		"cwd":             "/tmp/proj",
		"hook_event_name": "Stop",
	})
	return b
}

// runHookCmd invokes `qvr _hook <agent> <hookType>` with stdin via Cobra.
func runHookCmd(t *testing.T, stdin []byte, args ...string) (string, string, error) {
	t.Helper()
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

func TestHook_DisabledOps_NoOp(t *testing.T) {
	home, _ := isolatedHome(t, false)
	transcript, sid := writeTranscript(t, t.TempDir())
	_, _, err := runHookCmd(t, payloadFor(sid, transcript), "claude-code", "Stop")
	if err != nil {
		t.Errorf("disabled hook should be silent no-op; got err %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "skillops.db")); !os.IsNotExist(err) {
		t.Errorf("DB should not be created when ops disabled; got %v", err)
	}
}

func TestHook_Happy_CapturesRawAndDerivesSpans(t *testing.T) {
	_, readRaw := isolatedHome(t, true)
	transcript, sid := writeTranscript(t, t.TempDir())

	_, stderr, err := runHookCmd(t, payloadFor(sid, transcript), "claude-code", "Stop")
	if err != nil {
		t.Fatalf("hook: err=%v stderr=%q", err, stderr)
	}

	rows := readRaw()
	// 2 transcript lines + 1 hook payload.
	if len(rows) != 3 {
		t.Fatalf("expected 3 raw rows; got %d", len(rows))
	}
	var transcripts, hooks int
	for _, r := range rows {
		switch r.Source {
		case ops.RawSourceTranscript:
			transcripts++
		case ops.RawSourceHookPayload:
			hooks++
		}
		if r.AgentName != "claude-code" {
			t.Errorf("AgentName=%q want claude-code", r.AgentName)
		}
	}
	if transcripts != 2 || hooks != 1 {
		t.Errorf("want 2 transcript + 1 hook row; got %d + %d", transcripts, hooks)
	}

	// Spans were derived and persisted alongside raw.
	s, _ := store.Open(context.Background(), store.OpenOptions{Path: ops.DBPath(loadCfg(t))})
	defer s.Close()
	spans, err := s.QuerySpans(context.Background(), &store.SpanFilter{})
	if err != nil {
		t.Fatalf("query spans: %v", err)
	}
	if len(spans) == 0 {
		t.Error("expected derived spans to be persisted")
	}
}

func TestHook_EmptyStdin_NotSilent(t *testing.T) {
	_, _ = isolatedHome(t, true)
	_, stderr, err := runHookCmd(t, nil, "codex", "PreToolUse")
	if err != nil {
		t.Fatalf("hook: %v", err)
	}
	if !bytes.Contains([]byte(stderr), []byte("empty stdin")) {
		t.Errorf("empty stdin should warn on stderr; got %q", stderr)
	}
}

func TestHook_HookCommandIsHidden(t *testing.T) {
	if !hookCmd.Hidden {
		t.Error("_hook command should be hidden")
	}
}

func loadCfg(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
