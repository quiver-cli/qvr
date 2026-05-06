package store

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/raks097/quiver/internal/ops"
)

func mustParseTime(t *testing.T, s string) time.Time {
	t.Helper()
	got, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse time %q: %v", s, err)
	}
	return got
}

// TestBuild_EmptyFilter returns empty where/args.
func TestBuild_EmptyFilter(t *testing.T) {
	where, args := (&EventFilter{}).build()
	if where != "" {
		t.Errorf("expected empty where; got %q", where)
	}
	if args != nil {
		t.Errorf("expected nil args; got %v", args)
	}
}

// TestBuild_NilFilter is handled safely.
func TestBuild_NilFilter(t *testing.T) {
	var f *EventFilter
	where, args := f.build()
	if where != "" || args != nil {
		t.Errorf("expected nil-safe zero output")
	}
}

// TestBuild_EachDimension validates each filter dimension in isolation.
func TestBuild_EachDimension(t *testing.T) {
	since := mustParseTime(t, "2026-04-23T10:00:00Z")
	until := mustParseTime(t, "2026-04-24T10:00:00Z")
	sid := uuid.New()
	trueV := true
	falseV := false
	cursorTS := mustParseTime(t, "2026-04-23T12:00:00Z")
	cursorID := uuid.New()

	cases := []struct {
		name         string
		f            *EventFilter
		wantFragment string
		wantArgCount int
	}{
		{"Since", &EventFilter{Since: &since}, "timestamp >= ?", 1},
		{"Until", &EventFilter{Until: &until}, "timestamp <= ?", 1},
		{"Agents_Single", &EventFilter{Agents: []string{"claude"}}, "agent_name IN (?)", 1},
		{"Agents_Multi", &EventFilter{Agents: []string{"claude", "cursor"}}, "agent_name IN (?,?)", 2},
		{"Skills_Multi", &EventFilter{Skills: []string{"a", "b", "c"}}, "skill_name IN (?,?,?)", 3},
		{"Actions", &EventFilter{Actions: []ops.ActionType{ops.ActionFileRead, ops.ActionFileWrite}}, "action_type IN (?,?)", 2},
		{"Results", &EventFilter{Results: []ops.ResultStatus{ops.ResultSuccess}}, "result_status IN (?)", 1},
		{"FilePatterns", &EventFilter{FilePatterns: []string{"*.env", "*.pem"}}, "json_extract(payload, '$.path') GLOB ?", 2},
		{"CommandPatterns", &EventFilter{CommandPatterns: []string{"*rm*"}}, "json_extract(payload, '$.command') GLOB ?", 1},
		{"SessionID", &EventFilter{SessionID: &sid}, "session_id = ?", 1},
		{"IsSensitiveTrue", &EventFilter{IsSensitive: &trueV}, "is_sensitive = ?", 1},
		{"IsSensitiveFalse", &EventFilter{IsSensitive: &falseV}, "is_sensitive = ?", 1},
		{"Cursor", &EventFilter{Cursor: &Cursor{Timestamp: cursorTS, ID: cursorID}}, "timestamp < ? OR (timestamp = ? AND id < ?)", 3},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			where, args := tc.f.build()
			if !strings.Contains(where, tc.wantFragment) {
				t.Errorf("expected where to contain %q; got %q", tc.wantFragment, where)
			}
			if len(args) != tc.wantArgCount {
				t.Errorf("expected %d args; got %d (%v)", tc.wantArgCount, len(args), args)
			}
		})
	}
}

// TestBuild_PlaceholderCountMatchesArgs is the SQL-injection safety
// net: for every combination of dimensions we can build, the number
// of `?` in the output MUST equal the length of args.
func TestBuild_PlaceholderCountMatchesArgs(t *testing.T) {
	since := mustParseTime(t, "2026-04-23T00:00:00Z")
	until := mustParseTime(t, "2026-04-24T00:00:00Z")
	sid := uuid.New()
	trueV := true
	cursor := &Cursor{Timestamp: since, ID: uuid.New()}

	// Every non-empty subset of filter dimensions. 2^12 = 4096 combos.
	dims := []struct {
		name string
		set  func(f *EventFilter)
	}{
		{"since", func(f *EventFilter) { f.Since = &since }},
		{"until", func(f *EventFilter) { f.Until = &until }},
		{"agents", func(f *EventFilter) { f.Agents = []string{"a", "b"} }},
		{"skills", func(f *EventFilter) { f.Skills = []string{"x", "y", "z"} }},
		{"actions", func(f *EventFilter) { f.Actions = []ops.ActionType{ops.ActionFileRead} }},
		{"results", func(f *EventFilter) { f.Results = []ops.ResultStatus{ops.ResultSuccess, ops.ResultError} }},
		{"file", func(f *EventFilter) { f.FilePatterns = []string{"*.env", "*.key"} }},
		{"cmd", func(f *EventFilter) { f.CommandPatterns = []string{"*rm -rf*"} }},
		{"session", func(f *EventFilter) { f.SessionID = &sid }},
		{"sensitive", func(f *EventFilter) { f.IsSensitive = &trueV }},
		{"cursor", func(f *EventFilter) { f.Cursor = cursor }},
	}
	n := 1 << len(dims) // 2048

	for mask := 0; mask < n; mask++ {
		f := &EventFilter{}
		for i, d := range dims {
			if mask&(1<<i) != 0 {
				d.set(f)
			}
		}
		where, args := f.build()
		if got, want := countPlaceholders(where), len(args); got != want {
			parts := []string{}
			for i, d := range dims {
				if mask&(1<<i) != 0 {
					parts = append(parts, d.name)
				}
			}
			t.Fatalf("mask=%s: placeholder count %d != arg count %d\n  where: %s",
				strings.Join(parts, "+"), got, want, where)
		}
	}
}

// TestBuild_CombinesWithAnd verifies clauses join via AND.
func TestBuild_CombinesWithAnd(t *testing.T) {
	since := mustParseTime(t, "2026-04-23T00:00:00Z")
	f := &EventFilter{
		Since:   &since,
		Agents:  []string{"claude"},
		Actions: []ops.ActionType{ops.ActionFileRead},
	}
	where, _ := f.build()
	if strings.Count(where, " AND ") != 2 {
		t.Errorf("expected 2 AND joins; got %q", where)
	}
	if !strings.HasPrefix(where, "WHERE ") {
		t.Errorf("expected WHERE prefix; got %q", where)
	}
}

// TestBuild_NoInterpolation asserts user-provided strings can't
// escape into SQL. We feed a payload designed to break unquoted
// interpolation and check it appears only inside an arg.
func TestBuild_NoInterpolation(t *testing.T) {
	evil := "'; DROP TABLE audit_events;--"
	f := &EventFilter{
		Agents: []string{evil},
		Skills: []string{evil},
	}
	where, args := f.build()
	// Neither the evil payload nor a semicolon should appear in the
	// WHERE string itself.
	if strings.Contains(where, evil) {
		t.Errorf("user-provided value leaked into SQL: %q", where)
	}
	if strings.Contains(where, ";") {
		t.Errorf("WHERE clause contains stray semicolon: %q", where)
	}
	found := 0
	for _, a := range args {
		if s, ok := a.(string); ok && s == evil {
			found++
		}
	}
	if found != 2 {
		t.Errorf("expected evil value to appear in args twice; got %d", found)
	}
}

// TestBuild_CursorPredicateShape exercises the (timestamp,id) cursor
// inequality — it must be one clause with three bindings in the right
// order: timestamp, timestamp, id.
func TestBuild_CursorPredicateShape(t *testing.T) {
	ts := mustParseTime(t, "2026-04-23T00:00:00Z")
	id := uuid.New()
	f := &EventFilter{Cursor: &Cursor{Timestamp: ts, ID: id}}
	where, args := f.build()
	if strings.Count(where, "?") != 3 {
		t.Errorf("expected 3 placeholders; got %q", where)
	}
	if len(args) != 3 {
		t.Fatalf("expected 3 args; got %d", len(args))
	}
	gotID, ok := args[2].(string)
	if !ok || gotID != id.String() {
		t.Errorf("expected id string at args[2]; got %v", args[2])
	}
}

// TestBuild_FilePatternsOR multiple globs OR together inside one
// clause (not AND).
func TestBuild_FilePatternsOR(t *testing.T) {
	f := &EventFilter{FilePatterns: []string{"*.env", "*.pem"}}
	where, _ := f.build()
	if !strings.Contains(where, " OR ") {
		t.Errorf("expected OR between globs; got %q", where)
	}
	// The whole file-pattern group is wrapped in parens so it composes
	// correctly with other AND clauses.
	if !strings.Contains(where, "((json_extract") && !strings.Contains(where, "(json_extract") {
		t.Errorf("expected parenthesized group; got %q", where)
	}
}

// TestPlaceholders unit-tests the helper in isolation.
func TestPlaceholders(t *testing.T) {
	cases := map[int]string{
		0: "",
		1: "?",
		2: "?,?",
		3: "?,?,?",
		5: "?,?,?,?,?",
	}
	for n, want := range cases {
		if got := placeholders(n); got != want {
			t.Errorf("placeholders(%d)=%q want %q", n, got, want)
		}
	}
}

// TestEffectiveLimit returns the configured value or a safe default.
func TestEffectiveLimit(t *testing.T) {
	cases := map[int]int{
		0:   1000,
		-5:  1000,
		1:   1,
		100: 100,
	}
	for in, want := range cases {
		if got := (&EventFilter{Limit: in}).effectiveLimit(); got != want {
			t.Errorf("effectiveLimit(%d)=%d want %d", in, got, want)
		}
	}
	// Nil filter still returns the default.
	var nilF *EventFilter
	if got := nilF.effectiveLimit(); got != 1000 {
		t.Errorf("nil effectiveLimit got %d", got)
	}
}

// TestBuild_TimezoneNormalization all time args go in as UTC.
func TestBuild_TimezoneNormalization(t *testing.T) {
	pst, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	local := mustParseTime(t, "2026-04-23T10:00:00Z").In(pst)
	f := &EventFilter{Since: &local}
	_, args := f.build()
	got, ok := args[0].(time.Time)
	if !ok {
		t.Fatalf("expected time.Time; got %T", args[0])
	}
	if got.Location() != time.UTC {
		t.Errorf("expected UTC; got %s", got.Location())
	}
}
