package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedClaudeStore plants a claude-shaped session store inside a fake HOME (a
// skill-using transcript), so discover has something real to find.
func seedClaudeStore(t *testing.T, home string) {
	t.Helper()
	slug := filepath.Join(home, ".claude", "projects", "-tmp-proj")
	if err := os.MkdirAll(slug, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript, _ := writeTranscript(t, slug)
	// writeTranscript names the file session.jsonl; the store matches *.jsonl.
	_ = transcript
}

// TestAudit_Discover exercises the end-to-end command: scan a seeded store,
// record the skill session, and report it; a second run is a stat no-op.
func TestAudit_Discover(t *testing.T) {
	home, _ := isolatedHome(t, true)
	t.Setenv("HOME", home)
	seedClaudeStore(t, home)

	out, stderr, err := runRoot(t, nil, "audit", "discover", "--agent", "claude", "--output", "json")
	if err != nil {
		t.Fatalf("discover: err=%v stderr=%q", err, stderr)
	}
	var rep struct {
		Agents []struct {
			Agent    string `json:"agent"`
			Seen     int    `json:"seen"`
			Ingested int    `json:"ingested"`
		} `json:"agents"`
	}
	if e := json.Unmarshal([]byte(out), &rep); e != nil {
		t.Fatalf("decode discover json: %v\n%s", e, out)
	}
	if len(rep.Agents) != 1 || rep.Agents[0].Agent != "claude" ||
		rep.Agents[0].Seen != 1 || rep.Agents[0].Ingested != 1 {
		t.Fatalf("report = %+v, want claude seen=1 ingested=1", rep.Agents)
	}

	// The discovered session is queryable like any other.
	sessionsOut, _, err := runRoot(t, nil, "audit", "sessions", "--output", "json")
	if err != nil {
		t.Fatalf("sessions after discover: %v", err)
	}
	if !strings.Contains(sessionsOut, "code-review") {
		t.Errorf("discovered session missing skill attribution: %s", sessionsOut)
	}

	// Second run: unchanged store, nothing re-ingested.
	out2, _, err := runRoot(t, nil, "audit", "discover", "--agent", "claude", "--output", "json")
	if err != nil {
		t.Fatalf("second discover: %v", err)
	}
	var rep2 struct {
		Agents []struct {
			Unchanged int `json:"unchanged"`
			Ingested  int `json:"ingested"`
		} `json:"agents"`
	}
	if e := json.Unmarshal([]byte(out2), &rep2); e != nil {
		t.Fatalf("decode: %v", e)
	}
	if len(rep2.Agents) != 1 || rep2.Agents[0].Unchanged != 1 || rep2.Agents[0].Ingested != 0 {
		t.Errorf("second run = %+v, want all unchanged", rep2.Agents)
	}
}

// TestAudit_DiscoverEmptyStores pins the no-stores case: empty report, exit 0.
func TestAudit_DiscoverEmptyStores(t *testing.T) {
	home, _ := isolatedHome(t, true)
	t.Setenv("HOME", home)     // fake HOME with no agent stores at all
	t.Setenv("CODEX_HOME", "") // and no env-rooted stores leaking in from the host

	out, stderr, err := runRoot(t, nil, "audit", "discover", "--output", "json")
	if err != nil {
		t.Fatalf("discover on empty machine: err=%v stderr=%q", err, stderr)
	}
	var rep struct {
		Agents []struct {
			Seen int `json:"seen"`
		} `json:"agents"`
	}
	if e := json.Unmarshal([]byte(out), &rep); e != nil {
		t.Fatalf("decode: %v\n%s", e, out)
	}
	for _, a := range rep.Agents {
		if a.Seen != 0 {
			t.Errorf("empty machine should see 0 files, got %+v", rep.Agents)
		}
	}
}
