package model

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

var ErrVersionNotFound = errors.New("version not found")

// VersionKind distinguishes tags from branches.
type VersionKind string

const (
	VersionKindTag    VersionKind = "tag"
	VersionKindBranch VersionKind = "branch"
)

// Version represents a resolved version reference.
type Version struct {
	Ref       string      `json:"ref"`
	Kind      VersionKind `json:"kind"`
	Commit    string      `json:"commit"`
	IsSemver  bool        `json:"is_semver"`
	IsCurrent bool        `json:"is_current,omitempty"`
	IsDefault bool        `json:"is_default,omitempty"`
}

// VersionList holds all available versions for a skill in a registry.
type VersionList struct {
	SkillName     string    `json:"skill_name"`
	Registry      string    `json:"registry"`
	DefaultBranch string    `json:"default_branch,omitempty"`
	Current       string    `json:"current,omitempty"`
	Tags          []Version `json:"tags"`
	Branches      []Version `json:"branches"`
}

// SortVersions sorts tags by semver descending (newest first),
// non-semver tags alphabetically, then branches alphabetically
// with the default branch first.
func SortVersions(vl *VersionList, defaultBranch string) {
	sort.SliceStable(vl.Tags, func(i, j int) bool {
		if vl.Tags[i].IsSemver && vl.Tags[j].IsSemver {
			return compareSemver(vl.Tags[i].Ref, vl.Tags[j].Ref) > 0
		}
		if vl.Tags[i].IsSemver != vl.Tags[j].IsSemver {
			return vl.Tags[i].IsSemver
		}
		return vl.Tags[i].Ref < vl.Tags[j].Ref
	})

	sort.SliceStable(vl.Branches, func(i, j int) bool {
		if vl.Branches[i].Ref == defaultBranch {
			return true
		}
		if vl.Branches[j].Ref == defaultBranch {
			return false
		}
		return vl.Branches[i].Ref < vl.Branches[j].Ref
	})
}

// ResolveVersion finds a version by name.
// Resolution order: exact tag → exact branch → error.
// Empty string or "latest" resolves to the default branch HEAD.
func ResolveVersion(vl *VersionList, ref string, defaultBranch string) (*Version, error) {
	if ref == "" || ref == "latest" {
		for i := range vl.Branches {
			if vl.Branches[i].Ref == defaultBranch {
				return &vl.Branches[i], nil
			}
		}
		return nil, fmt.Errorf("%w: default branch %q not found", ErrVersionNotFound, defaultBranch)
	}

	for i := range vl.Tags {
		if vl.Tags[i].Ref == ref {
			return &vl.Tags[i], nil
		}
	}

	for i := range vl.Branches {
		if vl.Branches[i].Ref == ref {
			return &vl.Branches[i], nil
		}
	}

	return nil, fmt.Errorf("%w: %q", ErrVersionNotFound, ref)
}

// IsSemverTag checks if a tag name follows semver (v?MAJOR.MINOR.PATCH with optional pre-release).
func IsSemverTag(tag string) bool {
	s := strings.TrimPrefix(tag, "v")
	parts := strings.SplitN(s, "-", 2)
	nums := strings.Split(parts[0], ".")
	if len(nums) < 2 || len(nums) > 3 {
		return false
	}
	for _, n := range nums {
		if n == "" {
			return false
		}
		if _, err := strconv.Atoi(n); err != nil {
			return false
		}
	}
	return true
}

// compareSemver returns >0 if a > b, <0 if a < b, 0 if equal.
func compareSemver(a, b string) int {
	aParts := parseSemver(a)
	bParts := parseSemver(b)
	for i := 0; i < 3; i++ {
		if aParts[i] != bParts[i] {
			return aParts[i] - bParts[i]
		}
	}
	return 0
}

func parseSemver(s string) [3]int {
	s = strings.TrimPrefix(s, "v")
	parts := strings.SplitN(s, "-", 2) // strip pre-release
	nums := strings.Split(parts[0], ".")
	var result [3]int
	for i := 0; i < len(nums) && i < 3; i++ {
		result[i], _ = strconv.Atoi(nums[i])
	}
	return result
}
