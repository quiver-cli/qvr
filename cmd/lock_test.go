package cmd

import (
	"testing"

	"github.com/raks097/quiver/internal/config"
	"github.com/raks097/quiver/internal/model"
)

// Regression for #20 + #21: the message names actual failing categories
// rather than the legacy hardcoded "drift detected".
func TestFailureCategories_onlyNonZeroListed(t *testing.T) {
	cases := []struct {
		name string
		in   VerifySummary
		want string
	}{
		{"missing only", VerifySummary{Missing: 1}, "missing=1"},
		{"drift + missing", VerifySummary{Drift: 2, Missing: 1}, "drift=2, missing=1"},
		{"failed only", VerifySummary{Failed: 3}, "failed=3"},
		{"drift + failed", VerifySummary{Drift: 1, Failed: 2}, "drift=1, failed=2"},
		{"empty", VerifySummary{}, "no failing entries"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := failureCategories(c.in)
			if got != c.want {
				t.Errorf("failureCategories(%+v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// provenanceFromLegacyEntry is the v2→v3 upgrade-path inverse of what
// installer.go records on a fresh install. These tests pin its output so the
// upgraded entry's Provenance is byte-equivalent to a fresh `qvr install`
// against the same registry / ref / subpath (modulo timestamps + hash).
func TestProvenanceFromLegacyEntry_registrySource(t *testing.T) {
	entry := &model.LockEntry{
		Name:     "code-review",
		Registry: "raks",
		Branch:   "v0.2.0",
		Path:     "skills/code-review",
		Source:   "registry",
	}
	cfg := &config.Config{
		Registries: map[string]config.RegistryConfig{
			"raks": {URL: "https://github.com/raks097/skills.git"},
		},
	}
	got := provenanceFromLegacyEntry(entry, cfg)
	if got.RegistryName != "raks" {
		t.Errorf("RegistryName = %q, want %q", got.RegistryName, "raks")
	}
	if got.RegistryURL != "https://github.com/raks097/skills.git" {
		t.Errorf("RegistryURL = %q, want config URL", got.RegistryURL)
	}
	if got.Ref != "v0.2.0" {
		t.Errorf("Ref = %q, want %q", got.Ref, "v0.2.0")
	}
	if got.Subpath != "skills/code-review" {
		t.Errorf("Subpath = %q, want %q", got.Subpath, "skills/code-review")
	}
}

// Subdir installs (`qvr add <url>`) keep the canonical URL on the entry
// because there's no config.Registries record for them.
func TestProvenanceFromLegacyEntry_subdirSource(t *testing.T) {
	entry := &model.LockEntry{
		Name:     "x-article-editor",
		Registry: "openclaw-skills",
		RepoURL:  "https://github.com/openclaw/skills.git",
		Branch:   "main",
		Path:     "skills/jchopard69/x-article-editor",
		Source:   "subdir",
	}
	got := provenanceFromLegacyEntry(entry, &config.Config{})
	if got.RegistryURL != "https://github.com/openclaw/skills.git" {
		t.Errorf("RegistryURL = %q, want from entry.RepoURL", got.RegistryURL)
	}
	if got.RegistryName != "openclaw-skills" {
		t.Errorf("RegistryName = %q, want %q", got.RegistryName, "openclaw-skills")
	}
}

// An entry whose registry was removed from config still gets its name / ref
// / subpath backfilled — only the URL stays blank.
func TestProvenanceFromLegacyEntry_orphanRegistry(t *testing.T) {
	entry := &model.LockEntry{
		Name:     "foo",
		Registry: "gone",
		Branch:   "main",
		Path:     "skills/foo",
		Source:   "registry",
	}
	got := provenanceFromLegacyEntry(entry, &config.Config{
		Registries: map[string]config.RegistryConfig{},
	})
	if got.RegistryURL != "" {
		t.Errorf("RegistryURL = %q, want empty for orphan registry", got.RegistryURL)
	}
	if got.RegistryName != "gone" || got.Ref != "main" || got.Subpath != "skills/foo" {
		t.Errorf("non-URL fields lost on orphan registry: %+v", got)
	}
}

func TestMergeMissingProvenance_fillsBlanks(t *testing.T) {
	dst := model.ProvenanceRef{}
	src := model.ProvenanceRef{
		RegistryName: "raks",
		RegistryURL:  "https://example.invalid/raks.git",
		Ref:          "v0.2.0",
		Subpath:      "skills/code-review",
	}
	changed := mergeMissingProvenance(&dst, src)
	if !changed {
		t.Error("expected changed=true when dst was empty")
	}
	if dst != src {
		t.Errorf("dst = %+v, want %+v", dst, src)
	}
}

func TestMergeMissingProvenance_preservesExisting(t *testing.T) {
	// A partial entry where one field happens to be empty must NOT have its
	// already-set fields overwritten. Only the blank ones get filled.
	dst := model.ProvenanceRef{
		RegistryName: "intentional",
		RegistryURL:  "", // blank — should get filled
		Ref:          "explicit-ref",
	}
	src := model.ProvenanceRef{
		RegistryName: "different",
		RegistryURL:  "https://filled.invalid/x.git",
		Ref:          "different-ref",
		Subpath:      "skills/foo",
	}
	changed := mergeMissingProvenance(&dst, src)
	if !changed {
		t.Error("expected changed=true (RegistryURL and Subpath were blank)")
	}
	if dst.RegistryName != "intentional" {
		t.Errorf("RegistryName overwritten: %q", dst.RegistryName)
	}
	if dst.Ref != "explicit-ref" {
		t.Errorf("Ref overwritten: %q", dst.Ref)
	}
	if dst.RegistryURL != "https://filled.invalid/x.git" {
		t.Errorf("RegistryURL not filled: %q", dst.RegistryURL)
	}
	if dst.Subpath != "skills/foo" {
		t.Errorf("Subpath not filled: %q", dst.Subpath)
	}
}

func TestMergeMissingProvenance_noChangeWhenFull(t *testing.T) {
	dst := model.ProvenanceRef{
		RegistryName: "raks",
		RegistryURL:  "https://example.invalid/raks.git",
		Ref:          "v1",
		Subpath:      "skills/foo",
	}
	if mergeMissingProvenance(&dst, model.ProvenanceRef{RegistryName: "ignored"}) {
		t.Error("expected changed=false when no blanks remain")
	}
}
