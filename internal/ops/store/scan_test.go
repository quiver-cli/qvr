package store

import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"
)

func TestNullableString(t *testing.T) {
	if ns := nullableString(""); ns.Valid {
		t.Errorf("empty should be invalid")
	}
	ns := nullableString("x")
	if !ns.Valid || ns.String != "x" {
		t.Errorf("non-empty should be valid; got %+v", ns)
	}
}

func TestNullableJSON_NilAndEmpty(t *testing.T) {
	if nullableJSON(nil).Valid {
		t.Errorf("nil should be invalid")
	}
	if nullableJSON(json.RawMessage{}).Valid {
		t.Errorf("empty should be invalid")
	}
	ns := nullableJSON(json.RawMessage(`{"a":1}`))
	if !ns.Valid || ns.String != `{"a":1}` {
		t.Errorf("non-empty JSON: got %+v", ns)
	}
}

func TestNullableTime(t *testing.T) {
	if nullableTime(nil).Valid {
		t.Errorf("nil time should be invalid")
	}
	now := time.Now()
	nt := nullableTime(&now)
	if !nt.Valid {
		t.Errorf("non-nil time should be valid")
	}
	if nt.Time.Location() != time.UTC {
		t.Errorf("expected UTC; got %v", nt.Time.Location())
	}
}

func TestBoolToInt(t *testing.T) {
	if boolToInt(false) != 0 {
		t.Errorf("false→0")
	}
	if boolToInt(true) != 1 {
		t.Errorf("true→1")
	}
}

func TestEncodeSkillsTouched(t *testing.T) {
	if ns := encodeSkillsTouched(nil); ns.Valid {
		t.Errorf("nil skills should be invalid")
	}
	if ns := encodeSkillsTouched([]string{}); ns.Valid {
		t.Errorf("empty skills should be invalid")
	}
	ns := encodeSkillsTouched([]string{"a", "b"})
	if !ns.Valid {
		t.Fatalf("non-empty should be valid")
	}
	var got []string
	if err := json.Unmarshal([]byte(ns.String), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 2 || got[0] != "a" {
		t.Errorf("round-trip mismatch: %v", got)
	}
}

func TestParseSQLiteTime_Formats(t *testing.T) {
	cases := []string{
		"2026-04-24T16:56:01.36797Z",
		"2026-04-24T16:56:01Z",
		"2026-04-24 16:56:01.36797 +0000 UTC",
		"2026-04-24 16:56:01 +0000 UTC",
		"2026-04-24 16:56:01-07:00",
	}
	for _, s := range cases {
		t.Run(s, func(t *testing.T) {
			got, err := parseSQLiteTime(s)
			if err != nil {
				t.Errorf("unexpected err: %v", err)
			}
			if got.IsZero() {
				t.Errorf("expected non-zero; got zero")
			}
		})
	}
}

func TestParseSQLiteTime_RejectsGarbage(t *testing.T) {
	if _, err := parseSQLiteTime("not a time"); err == nil {
		t.Errorf("expected error")
	}
}

// nullable sentinel check: make sure sql.Null* produces the zero value
// we actually expect.
func TestSQLNullZero(t *testing.T) {
	var ns sql.NullString
	if ns.Valid {
		t.Errorf("zero NullString should be invalid")
	}
}
