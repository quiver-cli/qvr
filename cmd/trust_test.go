package cmd

import (
	"testing"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/model"
)

func TestVerifyTrustEntry_TrustedAuthor(t *testing.T) {
	cfg := &config.Config{Trust: config.TrustConfig{
		Registries: map[string]config.RegistryTrustConfig{
			"acme/skills": {Authors: []string{"Alice <alice@example.com>"}},
		},
	}}
	entry := &model.LockEntry{
		Name:       "review",
		Registry:   "acme/skills",
		Provenance: &model.ProvenanceRef{CommitAuthor: "Alice <alice@example.com>"},
	}

	got := verifyTrustEntry(entry, cfg)
	if got.Status != "trusted" {
		t.Fatalf("Status = %q, want trusted (row=%+v)", got.Status, got)
	}
}

func TestVerifyTrustEntry_EmailOnlyPinMatches(t *testing.T) {
	// Issue #172: pinning by email alone must gate by the author's email,
	// not silently reject because the stored identity is `Name <email>`.
	cfg := &config.Config{Trust: config.TrustConfig{
		Registries: map[string]config.RegistryTrustConfig{
			"acme/skills": {Authors: []string{"alice@example.com"}},
		},
	}}
	entry := &model.LockEntry{
		Name:       "review",
		Registry:   "acme/skills",
		Provenance: &model.ProvenanceRef{CommitAuthor: "Alice Dev <alice@example.com>"},
	}

	got := verifyTrustEntry(entry, cfg)
	if got.Status != "trusted" {
		t.Fatalf("Status = %q, want trusted (row=%+v)", got.Status, got)
	}
}

func TestVerifyTrustEntry_RejectsUnpinnedAuthor(t *testing.T) {
	cfg := &config.Config{Trust: config.TrustConfig{
		Registries: map[string]config.RegistryTrustConfig{
			"acme/skills": {Authors: []string{"Alice <alice@example.com>"}},
		},
	}}
	entry := &model.LockEntry{
		Name:       "review",
		Registry:   "acme/skills",
		Provenance: &model.ProvenanceRef{CommitAuthor: "Mallory <mallory@example.com>"},
	}

	got := verifyTrustEntry(entry, cfg)
	if got.Status != "failed" || got.Reason != "author not pinned" {
		t.Fatalf("row = %+v, want failed author not pinned", got)
	}
}

func TestVerifyTrustEntry_UnconfiguredRegistry(t *testing.T) {
	cfg := &config.Config{Trust: config.TrustConfig{Registries: map[string]config.RegistryTrustConfig{}}}
	entry := &model.LockEntry{Name: "review", Registry: "acme/skills"}

	got := verifyTrustEntry(entry, cfg)
	if got.Status != "unconfigured" {
		t.Fatalf("Status = %q, want unconfigured", got.Status)
	}
}
