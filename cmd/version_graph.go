package cmd

import (
	"strings"
	"time"

	"github.com/astra-sh/qvr/internal/git"
	"github.com/astra-sh/qvr/internal/ops/store"
)

// The version graph is the dashboard's git-tree view of a skill's history. Both
// the registry catalogue (branches/tags resolved to commits) and a skill's
// observed lineage (the versions that actually fired) render as a real
// multi-lane commit DAG, so this module turns a CommitGraph walk into one shared
// payload the frontend lays out into lanes. The flat `versions` array stays on
// each response as a graceful fallback for when the graph can't be built (the
// registry bare clone is unavailable, or the walk resolves nothing).

// versionGraphLimit bounds the ancestry walk so a deep registry history can't
// blow up a page load. Skill subtree histories are small; 200 is generous.
const versionGraphLimit = 200

// versionGraphRef is a branch or tag label pointing at a commit node.
type versionGraphRef struct {
	Name string `json:"name"`
	Kind string `json:"kind"` // "branch" | "tag"
}

// versionGraphUsage is the observed per-version rollup attached to a node in the
// lineage view — how that pinned commit behaved while it was installed. Token
// sides are nil when the sessions reported no usage (n/a, never a fabricated 0).
type versionGraphUsage struct {
	Invocations int64      `json:"invocations"`
	Sessions    int64      `json:"sessions"`
	FirstFired  *time.Time `json:"firstFired,omitempty"`
	LastFired   *time.Time `json:"lastFired,omitempty"`
	TokensIn    *int64     `json:"tokensIn,omitempty"`
	TokensOut   *int64     `json:"tokensOut,omitempty"`
}

// versionGraphNode is one commit in the DAG. Parents are the commit's parent
// hashes (lineage edges); a parent hash absent from the node set is a truncation
// root (the walk hit its limit). Refs decorate the catalogue view; Usage
// decorates the lineage view. Current marks the lock's pinned commit. The
// unknown-version bucket (invocations with no proven identity) is a detached
// node with an empty SHA and no parents.
type versionGraphNode struct {
	SHA     string             `json:"sha"`
	Parents []string           `json:"parents"`
	Refs    []versionGraphRef  `json:"refs,omitempty"`
	Time    *time.Time         `json:"time,omitempty"`
	Subject string             `json:"subject,omitempty"`
	Current bool               `json:"current,omitempty"`
	Usage   *versionGraphUsage `json:"usage,omitempty"`
}

// versionGraph is the shared payload: nodes carry their own edges (parents).
type versionGraph struct {
	Nodes []versionGraphNode `json:"nodes"`
}

// commitGrapher is the slice of *git.GoGitClient this module needs — declared
// here (consumer side) so the builders stay testable with a fake.
type commitGrapher interface {
	CommitGraph(repoPath string, tips []string, limit int) ([]git.CommitNode, error)
}

// baseNodes runs the ancestry walk and converts it to graph nodes, keyed by SHA
// for decoration. Returns nil when the walk fails or resolves nothing, so the
// caller falls back to the flat list.
func baseNodes(gc commitGrapher, repoPath string, tips []string) (map[string]*versionGraphNode, []string) {
	commits, err := gc.CommitGraph(repoPath, tips, versionGraphLimit)
	if err != nil || len(commits) == 0 {
		return nil, nil
	}
	byHash := make(map[string]*versionGraphNode, len(commits))
	order := make([]string, 0, len(commits))
	for _, c := range commits {
		when := c.Time
		node := &versionGraphNode{SHA: c.Hash, Parents: c.Parents, Subject: c.Subject}
		if !when.IsZero() {
			node.Time = &when
		}
		byHash[c.Hash] = node
		order = append(order, c.Hash)
	}
	return byHash, order
}

// collectNodes materializes the ordered node slice from the keyed map.
func collectNodes(byHash map[string]*versionGraphNode, order []string) []versionGraphNode {
	out := make([]versionGraphNode, 0, len(order))
	for _, h := range order {
		out = append(out, *byHash[h])
	}
	return out
}

// registryVersionGraph builds the catalogue DAG: every branch/tag tip walked to
// its ancestry, each node decorated with the refs that point at it and marked
// current when it carries the default-branch/installed ref. nil when the graph
// can't be built (caller keeps the flat list).
func registryVersionGraph(gc commitGrapher, repoPath string, vers []registryVersion) *versionGraph {
	if len(vers) == 0 {
		return nil
	}
	tips := make([]string, 0, len(vers))
	for _, v := range vers {
		if v.SHA != "" {
			tips = append(tips, v.SHA)
		}
	}
	byHash, order := baseNodes(gc, repoPath, tips)
	if byHash == nil {
		return nil
	}
	for _, v := range vers {
		node, ok := byHash[v.SHA]
		if !ok {
			continue // ref points outside the walked window
		}
		kind := "branch"
		if v.IsTag {
			kind = "tag"
		}
		node.Refs = append(node.Refs, versionGraphRef{Name: v.Ref, Kind: kind})
		if v.Current {
			node.Current = true
		}
	}
	return &versionGraph{Nodes: collectNodes(byHash, order)}
}

// lineageVersionGraph builds the observed-lineage DAG for one skill: the commits
// it actually fired as, placed in their true ancestry, each decorated with its
// usage rollup and marked current when it matches the lock's pin. Invocations
// with no proven commit (the unknown bucket) become a single detached node so
// the lineage stays honest. nil when nothing resolves.
func lineageVersionGraph(gc commitGrapher, repoPath, pinnedCommit string, versions []*store.SkillVersionUsage) *versionGraph {
	tips := make([]string, 0, len(versions))
	var unknown *versionGraphUsage
	for _, v := range versions {
		if v.Commit != "" {
			tips = append(tips, v.Commit)
		} else if v.Invocations > 0 {
			unknown = versionUsage(v)
		}
	}
	byHash, order := baseNodes(gc, repoPath, tips)
	if byHash == nil {
		if unknown == nil {
			return nil
		}
		// Only the unknown bucket fired (or no commit resolved): a lone detached
		// node still beats dropping the lineage to a flat list. Parents is an
		// explicit empty slice so it serializes as [] (not null) — the frontend
		// lane layout indexes/iterates parents directly.
		return &versionGraph{Nodes: []versionGraphNode{{SHA: "", Parents: []string{}, Usage: unknown}}}
	}
	for _, v := range versions {
		if v.Commit == "" {
			continue
		}
		// v.Commit is a 7-char short SHA when identity was proved via a store
		// worktree path; graph nodes are keyed by full SHA, so match by prefix.
		if node := findNodeByCommit(byHash, v.Commit); node != nil {
			node.Usage = versionUsage(v)
			if commitsMatch(pinnedCommit, node.SHA) {
				node.Current = true
			}
		}
	}
	nodes := collectNodes(byHash, order)
	if unknown != nil {
		// Explicit empty Parents → serializes as [] (not null); see above.
		nodes = append(nodes, versionGraphNode{SHA: "", Parents: []string{}, Usage: unknown})
	}
	return &versionGraph{Nodes: nodes}
}

// findNodeByCommit resolves a commit (full or 7-char short SHA) to its graph
// node: exact match first, then unique prefix. nil when nothing matches (the
// commit isn't in the walked window).
func findNodeByCommit(byHash map[string]*versionGraphNode, commit string) *versionGraphNode {
	if commit == "" {
		return nil
	}
	if node, ok := byHash[commit]; ok {
		return node
	}
	for sha, node := range byHash {
		if strings.HasPrefix(sha, commit) {
			return node
		}
	}
	return nil
}

// versionUsage adapts a store rollup row to the graph's usage shape.
func versionUsage(v *store.SkillVersionUsage) *versionGraphUsage {
	return &versionGraphUsage{
		Invocations: v.Invocations,
		Sessions:    v.Sessions,
		FirstFired:  msToTimePtr(v.FirstFiredMs),
		LastFired:   msToTimePtr(v.LastFiredMs),
		TokensIn:    v.InputTokens,
		TokensOut:   v.OutputTokens,
	}
}
