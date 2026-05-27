package model_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/raks097/quiver/internal/model"
)

func TestLockFile_v2LoadAndUpgrade(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, model.LockFileName)

	// Write a v2-shaped lockfile by hand. The on-disk format from a prior
	// release: version=2, no `verification` field anywhere.
	v2 := map[string]any{
		"version": 2,
		"skills": map[string]any{
			"foo": map[string]any{
				"name":        "foo",
				"registry":    "test-reg",
				"path":        "skills/foo",
				"branch":      "main",
				"commit":      "abcdef",
				"worktree":    "/some/worktree",
				"targets":     []string{"claude"},
				"global":      false,
				"source":      "registry",
				"installedAt": "2026-05-01T00:00:00Z",
				"updatedAt":   "2026-05-01T00:00:00Z",
			},
		},
	}
	raw, err := json.Marshal(v2)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(lockPath, raw, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Load → expect v2 entries to survive with Verification == nil.
	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if lock.Version != 2 {
		t.Errorf("loaded Version = %d, want 2 (preserved from disk)", lock.Version)
	}
	entry, err := lock.Get("foo")
	if err != nil {
		t.Fatalf("get foo: %v", err)
	}
	if entry.Verification != nil {
		t.Errorf("expected Verification == nil for v2 entry, got %+v", entry.Verification)
	}

	// Write → expect version bump to 3 on disk.
	if err := lock.Write(); err != nil {
		t.Fatalf("write: %v", err)
	}
	rereadRaw, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	var ondisk map[string]any
	if err := json.Unmarshal(rereadRaw, &ondisk); err != nil {
		t.Fatalf("unmarshal re-read: %v", err)
	}
	if v, _ := ondisk["version"].(float64); int(v) != model.LockFileVersion {
		t.Errorf("on-disk version = %v, want %d", ondisk["version"], model.LockFileVersion)
	}
	// Verification omitted by `omitempty` since it's nil — important so
	// existing entries don't pick up empty {} blocks on bump.
	skills, ok := ondisk["skills"].(map[string]any)
	if !ok {
		t.Fatalf("skills block missing or wrong type: %T", ondisk["skills"])
	}
	fooMap, ok := skills["foo"].(map[string]any)
	if !ok {
		t.Fatalf("foo entry missing or wrong type: %T", skills["foo"])
	}
	if _, present := fooMap["verification"]; present {
		t.Errorf("expected verification field to be omitted for nil block, found: %v", fooMap["verification"])
	}
}

func TestLockFile_v3RoundTrip(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, model.LockFileName)

	lock := model.NewLockFile(lockPath)
	now := time.Date(2026, 5, 27, 12, 0, 0, 0, time.UTC)
	lock.Put(&model.LockEntry{
		Name:     "foo",
		Registry: "test-reg",
		Path:     "skills/foo",
		Branch:   "main",
		Commit:   "abcdef",
		Worktree: "/x",
		Targets:  []string{"claude"},
		Verification: &model.VerificationRecord{
			SubtreeHash: "sha256:111",
			TreeSHA:     "tree-sha",
			CommitSHA:   "commit-sha",
			Provenance: model.ProvenanceRef{
				RegistryName: "test-reg",
				RegistryURL:  "https://example.invalid/test-reg.git",
				Ref:          "main",
				Subpath:      "skills/foo",
				FetchedAt:    now,
			},
			Status:     model.StatusUnverified,
			Warnings:   []string{"no signature present"},
			VerifiedAt: now,
		},
	})
	if err := lock.Write(); err != nil {
		t.Fatalf("write: %v", err)
	}

	reload, err := model.ReadLockFile(lockPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got, err := reload.Get("foo")
	if err != nil {
		t.Fatalf("get foo: %v", err)
	}
	if got.Verification == nil {
		t.Fatal("Verification block dropped on round-trip")
	}
	if got.Verification.SubtreeHash != "sha256:111" {
		t.Errorf("SubtreeHash = %q, want %q", got.Verification.SubtreeHash, "sha256:111")
	}
	if got.Verification.Status != model.StatusUnverified {
		t.Errorf("Status = %q, want %q", got.Verification.Status, model.StatusUnverified)
	}
	if got.Verification.Provenance.RegistryURL != "https://example.invalid/test-reg.git" {
		t.Errorf("Provenance.RegistryURL lost on round-trip")
	}
	if reload.Version != model.LockFileVersion {
		t.Errorf("Version = %d, want %d", reload.Version, model.LockFileVersion)
	}
}

func TestLockFile_optionalFieldsOmitted(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, model.LockFileName)

	// VerificationRecord without optional slots — Scan/Eval/Signature/etc.
	// should be omitted (nil), not serialized as null fields, so future
	// readers don't choke on unexpected keys.
	lock := model.NewLockFile(lockPath)
	lock.Put(&model.LockEntry{
		Name:     "foo",
		Registry: "r",
		Path:     "skills/foo",
		Branch:   "main",
		Verification: &model.VerificationRecord{
			SubtreeHash: "sha256:xyz",
			Status:      model.StatusUnverified,
			VerifiedAt:  time.Now().UTC(),
		},
	})
	if err := lock.Write(); err != nil {
		t.Fatalf("write: %v", err)
	}
	raw, _ := os.ReadFile(lockPath)
	body := string(raw)
	for _, field := range []string{`"scan"`, `"eval"`, `"signature"`, `"attestation"`, `"skillCard"`} {
		if indexOfStr(body, field) {
			t.Errorf("optional field %s present despite being nil — should be omitted", field)
		}
	}
}

func indexOfStr(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
