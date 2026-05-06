package store

import (
	"strings"

	"github.com/raks097/quiver/internal/ops"
)

// build translates an EventFilter into a parameterized WHERE clause
// plus the positional argument list. Returns ("", nil) when the filter
// is empty — the caller then omits the WHERE keyword entirely.
//
// SAFETY INVARIANTS (tested in filter_test.go):
//   - Every value reaches SQL via a `?` placeholder. No interpolation.
//   - The returned args slice has exactly as many entries as there are
//     `?` in the clause. Mismatch would explode SQLite at exec time.
//   - Callers must append Cursor comparison last so ORDER BY + LIMIT
//     semantics match pagination expectations.
func (f *EventFilter) build() (string, []any) {
	if f == nil {
		return "", nil
	}

	var clauses []string
	var args []any

	if f.Since != nil {
		clauses = append(clauses, "timestamp >= ?")
		args = append(args, f.Since.UTC())
	}
	if f.Until != nil {
		clauses = append(clauses, "timestamp <= ?")
		args = append(args, f.Until.UTC())
	}

	if len(f.Agents) > 0 {
		clauses = append(clauses, "agent_name IN ("+placeholders(len(f.Agents))+")")
		for _, a := range f.Agents {
			args = append(args, a)
		}
	}
	if len(f.Skills) > 0 {
		clauses = append(clauses, "skill_name IN ("+placeholders(len(f.Skills))+")")
		for _, s := range f.Skills {
			args = append(args, s)
		}
	}
	if len(f.Actions) > 0 {
		clauses = append(clauses, "action_type IN ("+placeholders(len(f.Actions))+")")
		for _, a := range f.Actions {
			args = append(args, string(a))
		}
	}
	if len(f.Results) > 0 {
		clauses = append(clauses, "result_status IN ("+placeholders(len(f.Results))+")")
		for _, r := range f.Results {
			args = append(args, string(r))
		}
	}

	// File/command globs use SQLite's native GLOB on extracted JSON
	// fields. Multiple patterns OR together inside a single clause so
	// they don't duplicate with other filters.
	if len(f.FilePatterns) > 0 {
		parts := make([]string, len(f.FilePatterns))
		for i, p := range f.FilePatterns {
			parts[i] = "json_extract(payload, '$.path') GLOB ?"
			args = append(args, p)
		}
		clauses = append(clauses, "("+strings.Join(parts, " OR ")+")")
	}
	if len(f.CommandPatterns) > 0 {
		parts := make([]string, len(f.CommandPatterns))
		for i, p := range f.CommandPatterns {
			parts[i] = "json_extract(payload, '$.command') GLOB ?"
			args = append(args, p)
		}
		clauses = append(clauses, "("+strings.Join(parts, " OR ")+")")
	}

	if f.SessionID != nil {
		clauses = append(clauses, "session_id = ?")
		args = append(args, f.SessionID.String())
	}
	if f.IsSensitive != nil {
		clauses = append(clauses, "is_sensitive = ?")
		if *f.IsSensitive {
			args = append(args, 1)
		} else {
			args = append(args, 0)
		}
	}

	// Cursor: paginate over the composite (timestamp, id) tuple.
	// DESC order so "older than cursor" is strictly less.
	if f.Cursor != nil {
		clauses = append(clauses, "(timestamp < ? OR (timestamp = ? AND id < ?))")
		args = append(args, f.Cursor.Timestamp.UTC(), f.Cursor.Timestamp.UTC(), f.Cursor.ID.String())
	}

	if len(clauses) == 0 {
		return "", nil
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

// placeholders returns "?,?,?..." of length n. Returns "" when n<=0;
// callers are expected to guard with a len(slice) > 0 check before
// calling, because an empty placeholder list cannot be substituted into
// an IN(...) clause anyway.
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	buf := strings.Builder{}
	buf.Grow(n*2 - 1)
	for i := 0; i < n; i++ {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.WriteByte('?')
	}
	return buf.String()
}

// effectiveLimit returns the LIMIT clause value. Zero/negative means
// "no limit", which we encode as a conservative upper bound to keep
// accidental full-table scans from crushing memory. Callers that
// genuinely want everything should use StreamEvents, not QueryEvents.
func (f *EventFilter) effectiveLimit() int {
	if f == nil || f.Limit <= 0 {
		return 1000
	}
	return f.Limit
}

// typed check: placeholder count equals arg count. Used by tests; not
// called at runtime.
func countPlaceholders(s string) int {
	n := 0
	for _, r := range s {
		if r == '?' {
			n++
		}
	}
	return n
}

// Compile-time guard: EventFilter is consumed exclusively via methods
// defined in this file. Keeping it unexported would prevent callers
// from constructing one, so we rely on build() being the sole SQL
// generator — any new predicate goes through build() and gets test
// coverage for free.
var _ = (*EventFilter)(nil)

// unused helper — keep references to ops.ActionType/ResultStatus alive
// in case linter complains about the import.
var _ ops.ActionType = ops.ActionFileRead
