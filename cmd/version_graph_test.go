package cmd

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/astra-sh/qvr/internal/git"
	"github.com/astra-sh/qvr/internal/ops/store"
)

// fakeGrapher returns a fixed commit DAG, so the version-graph builders can be
// tested without a real repository.
type fakeGrapher struct {
	nodes []git.CommitNode
	err   error
}

func (f fakeGrapher) CommitGraph(_ string, _ []string, _ int) ([]git.CommitNode, error) {
	return f.nodes, f.err
}

// diamond is the canonical test DAG: A → B, A → C, merge M of B+C.
func diamond() []git.CommitNode {
	return []git.CommitNode{
		{Hash: "m", Parents: []string{"b", "c"}, Subject: "merge"},
		{Hash: "c", Parents: []string{"a"}, Subject: "C"},
		{Hash: "b", Parents: []string{"a"}, Subject: "B"},
		{Hash: "a", Parents: nil, Subject: "A"},
	}
}

func nodeBySHA(g *versionGraph, sha string) *versionGraphNode {
	for i := range g.Nodes {
		if g.Nodes[i].SHA == sha {
			return &g.Nodes[i]
		}
	}
	return nil
}

func TestRegistryVersionGraph_DecoratesRefsAndCurrent(t *testing.T) {
	gc := fakeGrapher{nodes: diamond()}
	vers := []registryVersion{
		{Ref: "main", SHA: "m", Current: true},
		{Ref: "v1.0.0", IsTag: true, SHA: "a"},
	}
	g := registryVersionGraph(gc, "repo", vers)
	if g == nil {
		t.Fatal("expected a graph, got nil")
	}
	if len(g.Nodes) != 4 {
		t.Fatalf("expected 4 nodes, got %d", len(g.Nodes))
	}

	m := nodeBySHA(g, "m")
	if m == nil || !m.Current {
		t.Fatalf("node m missing or not current: %+v", m)
	}
	if len(m.Refs) != 1 || m.Refs[0].Name != "main" || m.Refs[0].Kind != "branch" {
		t.Errorf("node m refs = %+v, want one branch 'main'", m.Refs)
	}
	a := nodeBySHA(g, "a")
	if a == nil || len(a.Refs) != 1 || a.Refs[0].Kind != "tag" || a.Refs[0].Name != "v1.0.0" {
		t.Errorf("node a refs = %+v, want one tag 'v1.0.0'", a.Refs)
	}
	if a.Current {
		t.Errorf("node a should not be current")
	}
}

func TestRegistryVersionGraph_NilWhenWalkEmpty(t *testing.T) {
	gc := fakeGrapher{nodes: nil}
	if g := registryVersionGraph(gc, "repo", []registryVersion{{Ref: "main", SHA: "m"}}); g != nil {
		t.Errorf("expected nil graph when walk resolves nothing, got %+v", g)
	}
	if g := registryVersionGraph(gc, "repo", nil); g != nil {
		t.Errorf("expected nil graph for no versions, got %+v", g)
	}
}

func TestLineageVersionGraph_UsageCurrentAndUnknownBucket(t *testing.T) {
	gc := fakeGrapher{nodes: diamond()}
	versions := []*store.SkillVersionUsage{
		{Commit: "b", Invocations: 5, Sessions: 4},
		{Commit: "c", Invocations: 3, Sessions: 2},
		{Ref: "", Commit: "", Invocations: 2, Sessions: 1}, // unknown bucket
	}
	g := lineageVersionGraph(gc, "repo", "b", versions)
	require.NotNil(t, g)

	b := nodeBySHA(g, "b")
	require.NotNil(t, b)
	require.NotNil(t, b.Usage)
	require.Equal(t, int64(5), b.Usage.Invocations)
	require.True(t, b.Current, "node b matches the pinned commit")

	c := nodeBySHA(g, "c")
	require.NotNil(t, c)
	require.NotNil(t, c.Usage)
	require.Equal(t, int64(3), c.Usage.Invocations)
	require.False(t, c.Current)

	// Commit a fired no spans → no usage attached.
	a := nodeBySHA(g, "a")
	require.NotNil(t, a)
	require.Nil(t, a.Usage, "commit a fired no spans")

	// Unknown bucket → a detached node with empty SHA carrying its usage.
	unknown := nodeBySHA(g, "")
	require.NotNil(t, unknown)
	require.NotNil(t, unknown.Usage)
	require.Equal(t, int64(2), unknown.Usage.Invocations)
	require.Empty(t, unknown.Parents, "unknown node is detached")
}

func TestLineageVersionGraph_ShortCommitMatch(t *testing.T) {
	// Graph nodes carry full SHAs; observed versions carry 7-char short SHAs
	// (identity proved via a store worktree path). Usage must still attach.
	full := "abcdef1234567890abcdef1234567890abcdef12"
	gc := fakeGrapher{nodes: []git.CommitNode{{Hash: full, Parents: []string{}}}}
	versions := []*store.SkillVersionUsage{{Commit: full[:7], Invocations: 9, Sessions: 5}}

	g := lineageVersionGraph(gc, "repo", full[:7], versions)
	require.NotNil(t, g)
	n := nodeBySHA(g, full)
	require.NotNil(t, n)
	require.NotNil(t, n.Usage, "short-SHA version must decorate the full-SHA node")
	require.Equal(t, int64(9), n.Usage.Invocations)
	require.True(t, n.Current, "pinned short SHA must mark the node current")
}

func TestFindNodeByCommit_AmbiguousPrefixRefused(t *testing.T) {
	// Two nodes share the 7-char prefix "abcdef1": a short SHA of that prefix is
	// ambiguous and must resolve to neither (nil) rather than an arbitrary one.
	shaA := "abcdef1000000000000000000000000000000000"
	shaB := "abcdef1ffffffffffffffffffffffffffffffff0"
	byHash := map[string]*versionGraphNode{
		shaA: {SHA: shaA},
		shaB: {SHA: shaB},
	}
	require.Nil(t, findNodeByCommit(byHash, "abcdef1"), "ambiguous prefix must not decorate a node")
	// An exact full SHA still resolves.
	require.Equal(t, byHash[shaA], findNodeByCommit(byHash, shaA))
	// A unique prefix resolves.
	require.Equal(t, byHash[shaA], findNodeByCommit(byHash, "abcdef10"))
}

func TestLineageVersionGraph_OnlyUnknownStillRenders(t *testing.T) {
	gc := fakeGrapher{nodes: nil} // nothing resolves
	versions := []*store.SkillVersionUsage{{Commit: "", Invocations: 7}}
	g := lineageVersionGraph(gc, "repo", "", versions)
	if g == nil || len(g.Nodes) != 1 || g.Nodes[0].SHA != "" || g.Nodes[0].Usage.Invocations != 7 {
		t.Fatalf("expected lone unknown node, got %+v", g)
	}
}

func TestLineageVersionGraph_NilWhenNothing(t *testing.T) {
	gc := fakeGrapher{nodes: nil}
	if g := lineageVersionGraph(gc, "repo", "", nil); g != nil {
		t.Errorf("expected nil graph, got %+v", g)
	}
}
