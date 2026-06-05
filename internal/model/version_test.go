package model_test

import (
	"testing"

	"github.com/quiver-cli/qvr/internal/model"
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
