package modeltests

import (
	"errors"
	"testing"

	"github.com/raks097/quiver/internal/model"
)

func TestIsSemverTag(t *testing.T) {
	tests := []struct {
		tag  string
		want bool
	}{
		{"v1.0.0", true},
		{"1.2.3", true},
		{"v0.1.0", true},
		{"v10.20.30", true},
		{"v1.0", true},
		{"v1.0.0-alpha", true},
		{"v1.0.0-rc.1", true},
		{"main", false},
		{"release-v2", false},
		{"v1", false},
		{"v.1.0", false},
		{"", false},
		{"v", false},
		{"latest", false},
		{"v1.0.0.0", false},
	}

	for _, tt := range tests {
		t.Run(tt.tag, func(t *testing.T) {
			got := model.IsSemverTag(tt.tag)
			if got != tt.want {
				t.Errorf("IsSemverTag(%q) = %v, want %v", tt.tag, got, tt.want)
			}
		})
	}
}

func TestSortVersions(t *testing.T) {
	vl := &model.VersionList{
		Tags: []model.Version{
			{Ref: "v1.0.0", IsSemver: true},
			{Ref: "v2.0.0", IsSemver: true},
			{Ref: "v1.1.0", IsSemver: true},
			{Ref: "beta", IsSemver: false},
			{Ref: "alpha", IsSemver: false},
		},
		Branches: []model.Version{
			{Ref: "feature-x"},
			{Ref: "main"},
			{Ref: "develop"},
		},
	}

	model.SortVersions(vl, "main")

	// Semver tags should be descending: v2.0.0, v1.1.0, v1.0.0
	expectedTags := []string{"v2.0.0", "v1.1.0", "v1.0.0", "alpha", "beta"}
	for i, tag := range vl.Tags {
		if tag.Ref != expectedTags[i] {
			t.Errorf("Tags[%d] = %q, want %q", i, tag.Ref, expectedTags[i])
		}
	}

	// Default branch first, then alphabetical
	expectedBranches := []string{"main", "develop", "feature-x"}
	for i, branch := range vl.Branches {
		if branch.Ref != expectedBranches[i] {
			t.Errorf("Branches[%d] = %q, want %q", i, branch.Ref, expectedBranches[i])
		}
	}
}

func TestResolveVersion_ExactTag(t *testing.T) {
	vl := &model.VersionList{
		Tags:     []model.Version{{Ref: "v1.0.0", Kind: model.VersionKindTag, Commit: "aaa"}},
		Branches: []model.Version{{Ref: "main", Kind: model.VersionKindBranch, Commit: "bbb"}},
	}

	v, err := model.ResolveVersion(vl, "v1.0.0", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Ref != "v1.0.0" || v.Kind != model.VersionKindTag {
		t.Errorf("got %+v, want tag v1.0.0", v)
	}
}

func TestResolveVersion_ExactBranch(t *testing.T) {
	vl := &model.VersionList{
		Tags:     []model.Version{{Ref: "v1.0.0", Kind: model.VersionKindTag, Commit: "aaa"}},
		Branches: []model.Version{{Ref: "develop", Kind: model.VersionKindBranch, Commit: "bbb"}},
	}

	v, err := model.ResolveVersion(vl, "develop", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Ref != "develop" || v.Kind != model.VersionKindBranch {
		t.Errorf("got %+v, want branch develop", v)
	}
}

func TestResolveVersion_Latest(t *testing.T) {
	vl := &model.VersionList{
		Branches: []model.Version{
			{Ref: "main", Kind: model.VersionKindBranch, Commit: "aaa"},
			{Ref: "develop", Kind: model.VersionKindBranch, Commit: "bbb"},
		},
	}

	for _, ref := range []string{"", "latest"} {
		v, err := model.ResolveVersion(vl, ref, "main")
		if err != nil {
			t.Fatalf("ResolveVersion(%q): unexpected error: %v", ref, err)
		}
		if v.Ref != "main" {
			t.Errorf("ResolveVersion(%q) = %q, want main", ref, v.Ref)
		}
	}
}

func TestResolveVersion_TagPrecedence(t *testing.T) {
	// When a tag and branch share the same name, tag wins.
	vl := &model.VersionList{
		Tags:     []model.Version{{Ref: "release", Kind: model.VersionKindTag, Commit: "tag-hash"}},
		Branches: []model.Version{{Ref: "release", Kind: model.VersionKindBranch, Commit: "branch-hash"}},
	}

	v, err := model.ResolveVersion(vl, "release", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Kind != model.VersionKindTag {
		t.Errorf("expected tag, got %s", v.Kind)
	}
	if v.Commit != "tag-hash" {
		t.Errorf("expected tag-hash, got %s", v.Commit)
	}
}

func TestResolveVersion_NotFound(t *testing.T) {
	vl := &model.VersionList{
		Tags:     []model.Version{{Ref: "v1.0.0"}},
		Branches: []model.Version{{Ref: "main"}},
	}

	_, err := model.ResolveVersion(vl, "nonexistent", "main")
	if !errors.Is(err, model.ErrVersionNotFound) {
		t.Errorf("expected ErrVersionNotFound, got %v", err)
	}
}

func TestResolveVersion_LatestNoDefaultBranch(t *testing.T) {
	vl := &model.VersionList{
		Branches: []model.Version{{Ref: "develop"}},
	}

	_, err := model.ResolveVersion(vl, "latest", "main")
	if !errors.Is(err, model.ErrVersionNotFound) {
		t.Errorf("expected ErrVersionNotFound, got %v", err)
	}
}
