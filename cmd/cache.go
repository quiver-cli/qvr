package cmd

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/output"
	"github.com/astra-sh/qvr/internal/registry"
	"github.com/astra-sh/qvr/internal/skill"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// stdinIsTTYFn is the package-level seam tests use to drive the
// `qvr cache prune` confirmation gate without inheriting the test
// runner's actual TTY status. Real callers see the term.IsTerminal
// check; tests override to return false (non-TTY = CI / pipeline path).
//
// term.IsTerminal calls into platform-specific tty ioctls (TIOCGWINSZ
// on Unix, GetConsoleMode on Windows), which correctly distinguish a
// real terminal from a char device like /dev/null (issue #115 —
// os.ModeCharDevice alone falsely flagged /dev/null as a TTY, so a
// `qvr cache prune` running under cron/systemd with stdin closed
// hit the prompt path, read EOF, aborted silently with exit 0).
var stdinIsTTYFn = func() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// stdinIsTTY reports whether stdin is attached to a terminal. The
// character-device mode bit is the cross-platform indicator
// (linux/darwin tty, windows console). Used by `qvr cache prune` to
// gate the destructive op: on a TTY we can prompt; off one we refuse
// without --yes.
func stdinIsTTY() bool { return stdinIsTTYFn() }

// confirmYesNo prints prompt to stderr (so it doesn't pollute --output
// json's stdout) and returns true iff the user answers y/yes. Anything
// else — including EOF, an empty line, or a read error — is "no".
func confirmYesNo(prompt string) bool {
	fmt.Fprint(os.Stderr, prompt)
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return false
	}
	resp := strings.ToLower(strings.TrimSpace(scanner.Text()))
	return resp == "y" || resp == "yes"
}

var cacheCmd = &cobra.Command{
	Use:   "cache",
	Short: "Inspect and clean the shared worktree cache",
	Long: `Tools for the shared SHA-keyed worktree cache at ~/.quiver/worktrees/.

  qvr cache list             show reachable + orphan worktrees with sizes
  qvr cache prune --dry-run  show what prune would remove
  qvr cache prune            delete orphan worktrees
  qvr cache clean            wipe ALL worktrees (then re-run qvr sync)`,
	// Mirror lockCmd's "unknown subcommand" handling so a typo'd subcommand
	// (e.g. muscle-memory from npm/cargo) exits non-zero instead of printing
	// help with exit 0 — same shape as `qvr lock <typo>`. Issues #120, #169.
	RunE: rejectUnknownSubcommand,
}

var (
	cachePruneDryRun bool
	cachePruneYes    bool

	cacheCleanDryRun     bool
	cacheCleanYes        bool
	cacheCleanRegistries bool
)

var cacheListCmd = &cobra.Command{
	Use:   "list",
	Short: "Show reachable and orphan worktrees in the shared cache",
	RunE:  runCacheList,
}

var cachePruneCmd = &cobra.Command{
	Use:   "prune",
	Short: "Delete worktrees no longer referenced by any project lock",
	Long: `Walk ~/.quiver/worktrees/, cross-reference against every known project
lock (recorded in projects.json) and the user-global lock, and remove any
worktree directory that no lock entry still references. Also forgets project
entries whose lock files have vanished.

Then sweep the derived caches that back fast materialization — the
content-addressed blob store (cache/blobs) and the global identity/provenance
memos (cache/identity, cache/provenance) — dropping any record no installed
skill references. These are reconstructible (rebuilt on the next install), so
they're pruned without a separate prompt.

Use --dry-run to see the targets without deleting anything.`,
	RunE: runCachePrune,
}

var cacheCleanCmd = &cobra.Command{
	Use:   "clean",
	Short: "Wipe the entire worktree cache (reachable and orphan alike)",
	Long: `Remove every worktree under ~/.quiver/worktrees/ — both orphans and the
ones your installed skills currently point at — plus the registry index cache
at ~/.quiver/cache/index/. This is the "re-resolve from scratch" verb (mirrors
` + "`uv cache clean`" + `); unlike ` + "`qvr cache prune`" + `, which only deletes orphans.

Bare registry clones (~/.quiver/registries/) are kept by default — they are the
network-expensive artifact, and a subsequent ` + "`qvr sync`" + ` rebuilds every
worktree from them with zero network. Pass --registries to drop those too for a
full wipe.

After a clean the agent symlinks dangle until you run ` + "`qvr sync`" + `, which
restores every worktree the lock references.

Use --dry-run to preview. The wipe needs --yes when stdin isn't a TTY (CI).`,
	RunE: runCacheClean,
}

func init() {
	cachePruneCmd.Flags().BoolVar(&cachePruneDryRun, "dry-run", false,
		"report what would be removed without touching disk")
	cachePruneCmd.Flags().BoolVar(&cachePruneYes, "yes", false,
		"confirm the destructive prune non-interactively (required for non-TTY callers — issue #110)")
	cacheCleanCmd.Flags().BoolVar(&cacheCleanDryRun, "dry-run", false,
		"report what would be removed without touching disk")
	cacheCleanCmd.Flags().BoolVar(&cacheCleanYes, "yes", false,
		"confirm the destructive wipe non-interactively (required for non-TTY callers)")
	cacheCleanCmd.Flags().BoolVar(&cacheCleanRegistries, "registries", false,
		"also remove the bare registry clones (~/.quiver/registries/) — forces a re-clone on next sync")
	cacheCmd.AddCommand(cacheListCmd)
	cacheCmd.AddCommand(cachePruneCmd)
	cacheCmd.AddCommand(cacheCleanCmd)
	rootCmd.AddCommand(cacheCmd)
}

// CacheEntry describes one worktree in the cache, used by both list and
// prune output. Reachable is true when the worktree is referenced by at
// least one known lock file.
type CacheEntry struct {
	Path      string `json:"path"`
	Reachable bool   `json:"reachable"`
	SizeBytes int64  `json:"sizeBytes"`
}

// CacheListOutput is the JSON envelope for `qvr cache list`.
type CacheListOutput struct {
	Entries         []CacheEntry `json:"entries"`
	TotalBytes      int64        `json:"totalBytes"`
	OrphanBytes     int64        `json:"orphanBytes"`
	MissingProjects []string     `json:"missingProjects,omitempty"`
}

// CachePruneOutput is the JSON envelope for `qvr cache prune`.
//
// Removed/FreedBytes populate on a real prune; WouldRemove/WouldFree
// populate on --dry-run (issue #122). Pre-fix the dry-run path reused
// the `removed`/`freedBytes` names, so a scriptable consumer reading
// `removed` after a dry-run would think the prune ran — a PagerDuty
// footgun under pressure. The field-name split is the on-disk contract.
type CachePruneOutput struct {
	Removed        []string `json:"removed,omitempty"`
	WouldRemove    []string `json:"wouldRemove,omitempty"`
	ForgottenProjs []string `json:"forgottenProjects,omitempty"`
	FreedBytes     int64    `json:"freedBytes,omitempty"`
	WouldFree      int64    `json:"wouldFree,omitempty"`
	DryRun         bool     `json:"dryRun"`
	// MissingProjects covers project lock files that vanished — surfaced
	// in both list and prune output. List used to print these only as
	// trailing `! …` warnings in text and as a top-level JSON field;
	// prune merges them into the count via ForgottenProjs after the run.
	MissingProjects []string `json:"missingProjects,omitempty"`
	Errors          []string `json:"errors,omitempty"`
	// Derived-cache sweep (reconstructible memos backing fast materialization):
	// the content-store blobs and the global identity / provenance caches. Their
	// reclaimed bytes are folded into FreedBytes / WouldFree; these counts are
	// the per-cache detail.
	IdentityRemoved   int `json:"identityRemoved,omitempty"`
	ProvenanceRemoved int `json:"provenanceRemoved,omitempty"`
	BlobsRemoved      int `json:"blobsRemoved,omitempty"`
}

// CacheCleanOutput is the JSON envelope for `qvr cache clean`. It mirrors
// CachePruneOutput's Removed/WouldRemove split (issue #122) so a dry-run can
// never be mistaken for a real wipe by a scriptable consumer. IncludedRegistries
// records whether the bare clones were dropped too (--registries).
type CacheCleanOutput struct {
	Removed            []string `json:"removed,omitempty"`
	WouldRemove        []string `json:"wouldRemove,omitempty"`
	FreedBytes         int64    `json:"freedBytes,omitempty"`
	WouldFree          int64    `json:"wouldFree,omitempty"`
	DryRun             bool     `json:"dryRun"`
	IncludedRegistries bool     `json:"includedRegistries"`
	Errors             []string `json:"errors,omitempty"`
}

func runCacheList(cmd *cobra.Command, args []string) error {
	_ = args
	entries, missing, err := collectCacheEntries()
	if err != nil {
		return err
	}
	out := CacheListOutput{
		Entries:         entries,
		MissingProjects: missing,
	}
	for _, e := range entries {
		out.TotalBytes += e.SizeBytes
		if !e.Reachable {
			out.OrphanBytes += e.SizeBytes
		}
	}
	if printer.Format == output.FormatJSON {
		return printer.JSON(out)
	}
	if len(entries) == 0 {
		printer.Info("No worktrees in cache.")
		return nil
	}
	rows := make([][]string, 0, len(entries))
	for _, e := range entries {
		// Pre-#122 reachable/ORPHAN mixed case in the same column.
		// Both lowercase so the column reads cleanly.
		state := "reachable"
		if !e.Reachable {
			state = "orphan"
		}
		rows = append(rows, []string{state, humanBytes(e.SizeBytes), shortenCachePath(e.Path)})
	}
	printer.Table([]string{"STATE", "SIZE", "PATH"}, rows)
	// Fold vanished-project count into the summary line so text users
	// see the same signal JSON consumers get via missingProjects (issue
	// #122). Trailing per-project `! …` warnings still print so users
	// can act on the specific paths.
	summary := fmt.Sprintf("Total: %s (%s orphan", humanBytes(out.TotalBytes), humanBytes(out.OrphanBytes))
	if len(missing) > 0 {
		summary += fmt.Sprintf(", %d vanished project(s)", len(missing))
	}
	summary += ")"
	printer.Info(summary)
	for _, miss := range missing {
		printer.Warning(fmt.Sprintf("project lock vanished: %s (run `qvr cache prune` to forget)", miss))
	}
	return nil
}

func runCachePrune(cmd *cobra.Command, args []string) error {
	_ = args
	entries, missing, err := collectCacheEntries()
	if err != nil {
		return err
	}
	out := CachePruneOutput{
		DryRun:          cachePruneDryRun,
		MissingProjects: missing,
	}

	// First pass: pick out the orphans + the bytes they hold so we can
	// summarise them for the confirmation gate before any disk touch.
	var orphans []CacheEntry
	var orphanBytes int64
	for _, e := range entries {
		if e.Reachable {
			continue
		}
		orphans = append(orphans, e)
		orphanBytes += e.SizeBytes
	}

	if abort, err := confirmCachePrune(orphans, orphanBytes); err != nil || abort {
		return err
	}

	pruneCacheOrphans(&out, orphans, missing)

	if printer.Format == output.FormatJSON {
		if jerr := printer.JSON(out); jerr != nil {
			return jerr
		}
		if len(out.Errors) > 0 {
			// The body already carries the error list; suppress Execute()'s
			// {"error": "..."} envelope so stdout stays a single JSON doc
			// while exit code still signals failure.
			return errJSONHandled
		}
		return nil
	}
	renderCachePrune(out)
	if len(out.Errors) > 0 {
		return fmt.Errorf("cache prune: %d removal(s) failed", len(out.Errors))
	}
	return nil
}

// confirmCachePrune enforces prune's confirmation gate (issue #110): --yes is
// affirmative consent; missing it on a TTY prompts; missing it off a TTY (CI,
// pipelines) refuses. Dry-run bypasses since nothing actually deletes. Returns
// abort=true when the user declined (caller exits 0) and a non-nil error when
// consent is refused outright.
func confirmCachePrune(orphans []CacheEntry, orphanBytes int64) (abort bool, err error) {
	if cachePruneDryRun || len(orphans) == 0 || cachePruneYes {
		return false, nil
	}
	if printer.Format != output.FormatJSON {
		printer.Info(fmt.Sprintf("Would remove %d orphan worktree(s), freeing %s:",
			len(orphans), humanBytes(orphanBytes)))
		for _, e := range orphans {
			printer.Info(fmt.Sprintf("  - %s (%s)", shortenCachePath(e.Path), humanBytes(e.SizeBytes)))
		}
	}
	if printer.Format == output.FormatJSON || !stdinIsTTY() {
		return false, fmt.Errorf("refuse to delete %d orphan worktree(s) without --yes; pass --yes to confirm or --dry-run to preview (issue #110)", len(orphans))
	}
	if !confirmYesNo("Proceed? [y/N] ") {
		printer.Info("Aborted.")
		return true, nil
	}
	return false, nil
}

// pruneCacheOrphans removes each orphan worktree, forgets vanished projects, and
// sweeps the derived (blob/identity/provenance) caches. Dry-run records would-
// remove without touching disk (#122); real-run only counts a removal when it
// succeeds. Results accumulate on out.
func pruneCacheOrphans(out *CachePruneOutput, orphans []CacheEntry, missing []string) {
	for _, e := range orphans {
		// In dry-run we record the would-remove without touching disk —
		// under WouldRemove/WouldFree so a consumer can't confuse it with
		// a real deletion (issue #122). In real-run we only count
		// Removed + FreedBytes when the delete actually succeeded —
		// otherwise FreedBytes would lie and CI scripts couldn't tell
		// whether their pruning attempt worked.
		if cachePruneDryRun {
			out.WouldRemove = append(out.WouldRemove, e.Path)
			out.WouldFree += e.SizeBytes
			continue
		}
		if err := os.RemoveAll(e.Path); err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("remove %s: %v", e.Path, err))
			continue
		}
		out.Removed = append(out.Removed, e.Path)
		out.FreedBytes += e.SizeBytes
	}
	if !cachePruneDryRun {
		for _, m := range missing {
			registry.ForgetProject(m)
			out.ForgottenProjs = append(out.ForgottenProjs, m)
		}
	}

	// Sweep the derived caches (content-store blobs + identity/provenance memos)
	// of anything no installed skill references. These are reconstructible, so no
	// confirmation is needed; their reclaimed bytes fold into the freed total.
	if aux, auxErr := skill.PruneAuxCaches(cachePruneDryRun); auxErr != nil {
		out.Errors = append(out.Errors, fmt.Sprintf("prune derived caches: %v", auxErr))
	} else {
		out.IdentityRemoved = aux.IdentityRemoved
		out.ProvenanceRemoved = aux.ProvenanceRemoved
		out.BlobsRemoved = aux.BlobsRemoved
		if cachePruneDryRun {
			out.WouldFree += aux.FreedBytes
		} else {
			out.FreedBytes += aux.FreedBytes
		}
	}
}

// renderCachePrune prints the text-mode prune summary — Removed for a real run,
// WouldRemove for dry-run (#122) — the derived-cache record tally, the freed-byte
// success line, forgotten projects, and any per-removal errors.
func renderCachePrune(out CachePruneOutput) {
	activeList := out.Removed
	activeBytes := out.FreedBytes
	verb := "Removed"
	if cachePruneDryRun {
		activeList = out.WouldRemove
		activeBytes = out.WouldFree
		verb = "Would remove"
	}
	auxRecords := out.IdentityRemoved + out.ProvenanceRemoved + out.BlobsRemoved
	if len(activeList) == 0 && auxRecords == 0 {
		printer.Info("Nothing to prune.")
	} else {
		for _, p := range activeList {
			printer.Info(fmt.Sprintf("%s %s", verb, shortenCachePath(p)))
		}
		if auxRecords > 0 {
			printer.Info(fmt.Sprintf("%s %d blob(s), %d identity + %d provenance record(s)",
				verb, out.BlobsRemoved, out.IdentityRemoved, out.ProvenanceRemoved))
		}
		printer.Success(fmt.Sprintf("%s %d worktree(s) + %d cache record(s), freeing %s",
			verb, len(activeList), auxRecords, humanBytes(activeBytes)))
	}
	if !cachePruneDryRun {
		for _, m := range out.ForgottenProjs {
			printer.Info(fmt.Sprintf("Forgot vanished project %s", m))
		}
	}
	for _, e := range out.Errors {
		printer.Error(e)
	}
}

// cleanTarget is one directory the wipe will remove, paired with its on-disk
// size so we can report freed bytes accurately before and after deletion.
type cleanTarget struct {
	path  string
	label string // shortened path for display / the Removed list
	bytes int64
}

func runCacheClean(cmd *cobra.Command, args []string) error {
	_ = cmd
	_ = args
	out := CacheCleanOutput{
		DryRun:             cacheCleanDryRun,
		IncludedRegistries: cacheCleanRegistries,
	}

	// Enumerate every worktree leaf (reachable AND orphan — unlike prune) so
	// freed-byte accounting and the itemized Removed list are accurate. A
	// reachability read failure is non-fatal here: clean removes everything
	// regardless of reachability, so we fall back to a direct walk.
	entries, _, err := collectCacheEntries()
	if err != nil {
		// Reachability failed; size the worktrees root directly so the wipe
		// can still proceed (clean doesn't care which entries are reachable).
		if size, derr := dirSize(registry.WorktreesRoot()); derr == nil {
			entries = []CacheEntry{{Path: registry.WorktreesRoot(), SizeBytes: size}}
		}
	}

	targets, registriesRoot := buildCleanTargets(entries)

	var totalBytes int64
	for _, t := range targets {
		totalBytes += t.bytes
	}

	if abort, err := confirmCacheClean(targets, totalBytes); err != nil || abort {
		return err
	}

	executeCacheClean(&out, targets, registriesRoot)

	if printer.Format == output.FormatJSON {
		if jerr := printer.JSON(out); jerr != nil {
			return jerr
		}
		if len(out.Errors) > 0 {
			// Body already carries the error list; suppress Execute()'s
			// {"error": "..."} envelope so stdout stays one JSON doc while
			// the exit code still signals failure.
			return errJSONHandled
		}
		return nil
	}

	renderCacheClean(out)
	if len(out.Errors) > 0 {
		return fmt.Errorf("cache clean: %d removal(s) failed", len(out.Errors))
	}
	return nil
}

// executeCacheClean removes the three cache roots in one shot each (not per-leaf):
// a clean is a total wipe, so RemoveAll on the parent also sweeps up any stray
// non-leaf files. Enumerated worktree leaves are attributed to the worktrees-root
// removal so the Removed list stays itemized; index/registries report as single
// synthetic entries. Under dry-run nothing is deleted; counts only land on
// successful removals. Results accumulate on out.
func executeCacheClean(out *CacheCleanOutput, targets []cleanTarget, registriesRoot string) {
	removeRoot := func(root string, items []cleanTarget) {
		if root == "" {
			return
		}
		if cacheCleanDryRun {
			for _, it := range items {
				out.WouldRemove = append(out.WouldRemove, it.label)
				out.WouldFree += it.bytes
			}
			return
		}
		if err := os.RemoveAll(root); err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("remove %s: %v", shortenCachePath(root), err))
			return
		}
		for _, it := range items {
			out.Removed = append(out.Removed, it.label)
			out.FreedBytes += it.bytes
		}
	}

	// Partition targets by which root they belong to so each RemoveAll
	// attributes the right items (and only counts them on success).
	var worktreeItems, idxItems, regItems []cleanTarget
	for _, t := range targets {
		switch t.label {
		case "cache/index":
			idxItems = append(idxItems, t)
		case "registries":
			regItems = append(regItems, t)
		default:
			worktreeItems = append(worktreeItems, t)
		}
	}
	if len(worktreeItems) > 0 {
		removeRoot(registry.WorktreesRoot(), worktreeItems)
	}
	if len(idxItems) > 0 {
		removeRoot(registry.CacheDir(), idxItems)
	}
	if len(regItems) > 0 {
		removeRoot(registriesRoot, regItems)
	}
}

// buildCleanTargets enumerates every directory a clean will wipe: the worktree
// leaves, the always-derived registry index, the blob/identity/provenance memos,
// and (under --registries) the bare registry clones. It returns the targets plus
// the registries root path (empty when --registries wasn't passed) so the
// removal pass can attribute that root's items.
func buildCleanTargets(entries []CacheEntry) ([]cleanTarget, string) {
	var targets []cleanTarget
	for _, e := range entries {
		targets = append(targets, cleanTarget{path: e.Path, label: shortenCachePath(e.Path), bytes: e.SizeBytes})
	}
	// The registry index cache is always part of a clean — it's pure derived
	// state, rebuilt on the next registry refresh.
	if idx := registry.CacheDir(); dirExists(idx) {
		size, _ := dirSize(idx)
		targets = append(targets, cleanTarget{path: idx, label: "cache/index", bytes: size})
	}
	// The derived caches that back fast materialization are likewise pure
	// reconstructible state (rebuilt on the next install): the content-store
	// blobs and the global identity / provenance memos.
	for _, c := range []struct{ root, label string }{
		{registry.BlobStoreRoot(), "cache/blobs"},
		{registry.IdentityCacheRoot(), "cache/identity"},
		{registry.ProvenanceCacheRoot(), "cache/provenance"},
	} {
		if dirExists(c.root) {
			size, _ := dirSize(c.root)
			targets = append(targets, cleanTarget{path: c.root, label: c.label, bytes: size})
		}
	}
	var registriesRoot string
	if cacheCleanRegistries {
		registriesRoot = filepath.Join(config.Dir(), "registries")
		if dirExists(registriesRoot) {
			// Full size, not the hardlink-discounted dirSize: the bare clones
			// ARE the canonical copy of every object the worktrees hardlink.
			// Once --registries removes them (and clean removes the worktrees
			// too), those blocks are genuinely freed, so they belong in the
			// total. The worktree targets already counted their shared objects
			// as 0, so this attributes each shared block to the bare exactly
			// once — no double count (issue #158).
			size, _ := fullDirSize(registriesRoot)
			targets = append(targets, cleanTarget{path: registriesRoot, label: "registries", bytes: size})
		}
	}
	return targets, registriesRoot
}

// confirmCacheClean enforces the destructive-wipe confirmation gate (issue #110):
// --yes is affirmative consent; on a TTY without it we prompt; off a TTY or under
// --output json we refuse. Dry-run bypasses since nothing deletes. clean is more
// destructive than prune (it removes reachable worktrees too), so the prompt
// spells that out. Returns abort=true when the user declined (caller exits 0) and
// a non-nil error when consent is refused outright.
func confirmCacheClean(targets []cleanTarget, totalBytes int64) (abort bool, err error) {
	if cacheCleanDryRun || len(targets) == 0 || cacheCleanYes {
		return false, nil
	}
	if printer.Format != output.FormatJSON {
		printer.Info(fmt.Sprintf("Would wipe the entire cache (%d item(s), freeing %s), including worktrees your installed skills point at:",
			len(targets), humanBytes(totalBytes)))
		for _, t := range targets {
			printer.Info(fmt.Sprintf("  - %s (%s)", t.label, humanBytes(t.bytes)))
		}
		printer.Info("Run `qvr sync` afterwards to restore installed skills.")
	}
	if printer.Format == output.FormatJSON || !stdinIsTTY() {
		return false, fmt.Errorf("refuse to wipe the cache without --yes; pass --yes to confirm or --dry-run to preview")
	}
	if !confirmYesNo("Proceed? [y/N] ") {
		printer.Info("Aborted.")
		return true, nil
	}
	return false, nil
}

// renderCacheClean prints the text-mode clean summary — Removed for a real wipe,
// WouldRemove for dry-run — followed by any per-removal errors.
func renderCacheClean(out CacheCleanOutput) {
	activeList := out.Removed
	activeBytes := out.FreedBytes
	verb := "Removed"
	if cacheCleanDryRun {
		activeList = out.WouldRemove
		activeBytes = out.WouldFree
		verb = "Would remove"
	}
	if len(activeList) == 0 {
		printer.Info("Cache already empty.")
	} else {
		for _, p := range activeList {
			printer.Info(fmt.Sprintf("%s %s", verb, p))
		}
		printer.Success(fmt.Sprintf("%s %d item(s), freeing %s", verb, len(activeList), humanBytes(activeBytes)))
		if !cacheCleanDryRun {
			printer.Info("Run `qvr sync` to restore installed skills.")
		}
	}
	for _, e := range out.Errors {
		printer.Error(e)
	}
}

// dirExists reports whether path exists and is a directory.
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// collectCacheEntries walks the worktrees root and joins each leaf against the
// reachability set computed from every known project lock plus the global lock.
// Leaves are detected as "the directory contains either a .git directory or is
// already a git working tree" — matches what the installer creates.
func collectCacheEntries() ([]CacheEntry, []string, error) {
	reach, err := registry.Reachable()
	if err != nil {
		// A reachability read failure means nothing is "reachable" — every
		// worktree would be pruned. Refuse rather than silently nuke the
		// cache.
		return nil, nil, fmt.Errorf("compute reachability: %w", err)
	}

	// registry.WorktreeLeaves enumerates worktree roots config-independently
	// (legacy `.git` worktrees and worktree-free content dirs #204), so prune
	// reclaims true orphans even under a removed registry (#4) without leaking
	// (#221).
	var entries []CacheEntry
	for _, leaf := range registry.WorktreeLeaves() {
		size, _ := dirSize(leaf)
		entries = append(entries, CacheEntry{
			Path:      leaf,
			Reachable: isReachableLeaf(leaf, reach.Worktrees),
			SizeBytes: size,
		})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		// Orphans first so the most useful output is at the top of the table.
		if entries[i].Reachable != entries[j].Reachable {
			return !entries[i].Reachable
		}
		return entries[i].Path < entries[j].Path
	})
	return entries, reach.MissingProjects, nil
}

// isReachableLeaf reports whether a discovered worktree leaf must be PROTECTED
// from prune. A leaf is reachable when it exactly matches a referenced worktree
// root, OR overlaps one as an ancestor or descendant. The overlap check is a
// safety net for WorktreeLeaves' hash-name heuristic: if it ever mis-identifies
// the root level (e.g. a pathological all-hex registry/skill name, or a hash-like
// content subdir), the candidate is an ancestor/descendant of the true reachable
// root and is kept — so a misfire can only leak, never delete a live worktree
// (the data-loss regression #231). True orphans sit at sibling depth to every
// reachable root (they differ at the `<sha>` segment), so they never overlap and
// are still reclaimed.
func isReachableLeaf(leaf string, reachable map[string]struct{}) bool {
	if _, ok := reachable[leaf]; ok {
		return true
	}
	for root := range reachable {
		if strings.HasPrefix(leaf, root+string(os.PathSeparator)) ||
			strings.HasPrefix(root, leaf+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

// dirSize sums the on-disk size that deleting dir would actually reclaim:
// every regular file under it, counting hardlinked-and-shared object files
// (which a `git clone --local` worktree shares with the bare registry) as 0
// since removing the worktree won't free them. See reclaimableFileSize for
// the per-file rule and issue #158 for why a naive Size() sum lied by a large
// multiple. Best-effort — unreadable files contribute 0 rather than aborting.
func dirSize(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		total += reclaimableFileSize(info)
		return nil
	})
	return total, err
}

// fullDirSize sums the on-disk size of every regular file under dir WITHOUT
// the hardlink discount dirSize applies. Used for the bare-registry target in
// `qvr cache clean --registries`, where the bare holds the canonical copy of
// the shared object blocks and removing it genuinely frees them. Best-effort —
// unreadable files contribute 0 rather than aborting.
func fullDirSize(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		total += info.Size()
		return nil
	})
	return total, err
}

// shortenCachePath drops the QUIVER_HOME prefix so the printed table stays
// readable. ~/.quiver/worktrees/foo/bar/sha → worktrees/foo/bar/sha.
func shortenCachePath(p string) string {
	root := registry.WorktreesRoot()
	if after, ok := strings.CutPrefix(p, root); ok {
		return "worktrees" + after
	}
	return p
}

// humanBytes renders an int64 byte count as a short human-readable string.
// Resolution is intentionally coarse — we want "12 MB" not "12.345 MB".
func humanBytes(b int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)
	switch {
	case b >= GB:
		return fmt.Sprintf("%d GB", b/GB)
	case b >= MB:
		return fmt.Sprintf("%d MB", b/MB)
	case b >= KB:
		return fmt.Sprintf("%d KB", b/KB)
	default:
		return fmt.Sprintf("%d B", b)
	}
}
