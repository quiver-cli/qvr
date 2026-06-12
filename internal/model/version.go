package model

import (
	"sort"
	"strconv"
	"strings"
)

// VersionKind distinguishes tags from branches.
type VersionKind string

// The two VersionKind values: a ref is either a git tag or a branch.
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

// SkillTagSep separates the per-skill namespace from the version in a
// multi-skill registry tag: "<skill>/vX.Y.Z". qvr namespaces version tags per
// skill so two skills in one registry can both debut at the same semver without
// colliding on a repo-global tag (issue #152). Single-skill (root/fork) repos
// keep bare "vX.Y.Z" tags.
const SkillTagSep = "/"

// VersionPortion returns the version-comparable tail of a (possibly per-skill
// namespaced) tag: "alpha/v0.1.0" → "v0.1.0", bare "v0.1.0" → "v0.1.0". Used so
// every semver primitive treats a namespaced tag by its version part while
// leaving bare tags untouched. Splits on the LAST separator so a skill name
// that itself contains the separator (it can't today — names are
// hyphen/alphanumeric) would still yield the trailing version.
func VersionPortion(tag string) string {
	if i := strings.LastIndex(tag, SkillTagSep); i >= 0 {
		return tag[i+1:]
	}
	return tag
}

// TagBelongsToSkill reports whether a registry tag is a version of the named
// skill: either its per-skill-namespaced tag "<skill>/..." or a bare
// (un-namespaced) tag — the latter is what legacy single-skill repos produced
// and stays shared across the registry's skills. A tag namespaced for a
// different skill is excluded so two skills no longer claim each other's
// versions (issue #152).
func TagBelongsToSkill(tag, skill string) bool {
	if strings.HasPrefix(tag, skill+SkillTagSep) {
		return true
	}
	return !strings.Contains(tag, SkillTagSep)
}

// IsSemverTag checks if a tag name follows semver (v?MAJOR.MINOR.PATCH with
// optional pre-release). Per-skill-namespaced tags are judged by their version
// portion, so "alpha/v0.1.0" is semver (issue #152).
func IsSemverTag(tag string) bool {
	s := strings.TrimPrefix(VersionPortion(tag), "v")
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

// CompareSemver compares two semver strings (with or without a leading "v" or
// per-skill namespace prefix), returning >0 if a > b, <0 if a < b, 0 if equal.
// It is the exported entry point for callers outside this package — e.g.
// `qvr upgrade` comparing the running binary's version to the latest release —
// so semver ordering has a single authority here.
func CompareSemver(a, b string) int { return compareSemver(a, b) }

// compareSemver returns >0 if a > b, <0 if a < b, 0 if equal.
func compareSemver(a, b string) int {
	aParts := parseSemver(a)
	bParts := parseSemver(b)
	for i := range 3 {
		if aParts[i] != bParts[i] {
			return aParts[i] - bParts[i]
		}
	}
	return 0
}

func parseSemver(s string) [3]int {
	s = strings.TrimPrefix(VersionPortion(s), "v")
	parts := strings.SplitN(s, "-", 2) // strip pre-release
	nums := strings.Split(parts[0], ".")
	var result [3]int
	for i := 0; i < len(nums) && i < 3; i++ {
		result[i], _ = strconv.Atoi(nums[i])
	}
	return result
}
