package cmd

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/quiver-cli/qvr/internal/config"
	"github.com/quiver-cli/qvr/internal/manifest"
	"github.com/quiver-cli/qvr/internal/model"
	"github.com/quiver-cli/qvr/internal/output"
	"github.com/spf13/cobra"
)

var (
	exportOutputFile     string
	exportFrozen         bool
	exportIncludeAliases bool
	exportIncludeLocal   bool
	exportGlobal         bool
)

var exportCmd = &cobra.Command{
	Use:   "export",
	Short: "Emit a portable plain-text skill manifest from qvr.lock",
	Long: `Emit a portable manifest of the project's installed skills — one line per
skill, three columns (repo URL, skill name, version), plus optional flags.
The recipient runs ` + "`qvr import <file>`" + ` and gets the same skill set
without any pre-existing registry configuration.

  qvr export > skills.txt                              # default: ref only
  qvr export --frozen > skills.lock.txt                # pin to recorded commits
  qvr export --output-file=skills.txt --include-aliases

` + "`mode: edit`" + ` and ` + "`mode: link`" + ` skills aren't portable across
projects (they live in the project filesystem) and are skipped by default;
pass --include-local to emit them as commented documentation lines.`,
	RunE: runExport,
}

func init() {
	exportCmd.Flags().StringVar(&exportOutputFile, "output-file", "",
		"write the manifest to this path instead of stdout")
	exportCmd.Flags().BoolVar(&exportFrozen, "frozen", false,
		"emit --commit=<sha> pins from the current lock for bit-for-bit determinism")
	exportCmd.Flags().BoolVar(&exportIncludeAliases, "include-aliases", false,
		"emit --registry-alias=<name> per line so the importer preserves local registry names")
	exportCmd.Flags().BoolVar(&exportIncludeLocal, "include-local", false,
		"emit `mode: edit` / `mode: link` skills as commented documentation lines (not parsed by `qvr import`)")
	exportCmd.Flags().BoolVar(&exportGlobal, "global", false,
		"export from the user-global lock instead of the project lock")
	rootCmd.AddCommand(exportCmd)
}

// exportEntry is the JSON shape for `qvr export --output json`, mirroring the
// text manifest column-for-column so JSON consumers can rebuild the manifest
// without reparsing it.
type exportEntry struct {
	RepoURL       string   `json:"repoUrl"`
	Skill         string   `json:"skill"`
	Version       string   `json:"version"`
	Commit        string   `json:"commit,omitempty"`
	Targets       []string `json:"targets,omitempty"`
	Alias         string   `json:"alias,omitempty"`
	RegistryAlias string   `json:"registryAlias,omitempty"`
}

type exportPayload struct {
	Entries  []exportEntry `json:"entries"`
	Excluded []string      `json:"excluded,omitempty"`
}

func runExport(cmd *cobra.Command, args []string) error {
	_ = args
	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}
	lockPath := model.DefaultLockPath(projectRoot, config.Dir(), exportGlobal)
	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		return fmt.Errorf("read lock: %w", err)
	}

	entries, excluded, excludedDetails := buildExportEntries(lock)

	if len(excluded) > 0 && printer.Format != output.FormatJSON {
		printer.Warning(fmt.Sprintf("excluded %d non-portable skill(s): %s", len(excluded), strings.Join(excluded, ", ")))
	}

	// Choose the output sink. stdout is the default so `qvr export | …` is
	// the natural shape; --output-file routes to a file so users don't have
	// to remember shell redirection syntax in CI scripts.
	out := cmd.OutOrStdout()
	var fileSink *os.File
	if exportOutputFile != "" {
		f, ferr := os.Create(exportOutputFile)
		if ferr != nil {
			return fmt.Errorf("create %s: %w", exportOutputFile, ferr)
		}
		defer f.Close()
		fileSink = f
		out = f
	}

	if printer.Format == output.FormatJSON {
		payload := exportPayload{
			Entries:  toJSONEntries(entries),
			Excluded: excluded,
		}
		// JSON always goes to the printer's destination (stdout) — writing
		// JSON to --output-file at the same time would double-emit. If a
		// user really wants JSON in a file they redirect; --output-file is
		// for the text manifest case.
		if fileSink != nil {
			printer.Warning("--output-file is ignored in --output json mode; redirect stdout instead")
		}
		return printer.JSON(payload)
	}

	header := exportHeader()
	manifestEntries := toManifestEntries(entries)
	if err := manifest.Format(out, manifest.FormatOptions{Header: header, Align: true}, manifestEntries); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}

	if exportIncludeLocal && len(excludedDetails) > 0 {
		// Emit each excluded entry as a `# local: …` comment block so the
		// recipient still sees what was in the source project, even though
		// `qvr import` will ignore the lines. Keeps the documentation value
		// without compromising the parser contract.
		for _, d := range excludedDetails {
			fmt.Fprintf(out, "# local: %s (mode=%s, path=%s)\n", d.name, d.mode, d.path)
		}
	}
	return nil
}

func exportHeader() string {
	flags := []string{}
	if exportFrozen {
		flags = append(flags, "--frozen")
	}
	if exportIncludeAliases {
		flags = append(flags, "--include-aliases")
	}
	if exportIncludeLocal {
		flags = append(flags, "--include-local")
	}
	prefix := "qvr export"
	if len(flags) > 0 {
		prefix += " " + strings.Join(flags, " ")
	}
	return fmt.Sprintf("%s — %s\nformat: <repo-url>  <skill>  <version>  [flags]",
		prefix, time.Now().UTC().Format("2006-01-02"))
}

// excludedDetail is the per-skill record for --include-local rendering.
type excludedDetail struct {
	name string
	mode string
	path string
}

// buildExportEntries converts a lock into manifest entries plus a list of
// names that were excluded (link/edit modes). Entries are sorted by skill name
// for a stable diff.
func buildExportEntries(lock *model.LockFile) ([]exportEntry, []string, []excludedDetail) {
	var entries []exportEntry
	var excluded []string
	var excludedDetails []excludedDetail
	for _, e := range lock.Entries() {
		// Link and edit installs can't be re-resolved from a registry on
		// the recipient's side — the source is a local filesystem path
		// that won't exist over there. Skip with a list (the caller logs
		// the names to stderr) so the manifest stays portable.
		if e.IsLink() || e.IsEdit() {
			excluded = append(excluded, e.Name)
			excludedDetails = append(excludedDetails, excludedDetail{
				name: e.Name,
				mode: nonemptyMode(e),
				path: nonemptySource(e),
			})
			continue
		}
		// Disabled entries are still part of the lock; we emit them so the
		// recipient ends up with the same set of installed skills. The
		// disabled state itself is project-local UX and not part of the
		// portable shape.
		if e.Source == "" {
			// Defensive: a v5 entry with no Source can't be installed
			// remotely. Treat the same as a link install.
			excluded = append(excluded, e.Name)
			excludedDetails = append(excludedDetails, excludedDetail{
				name: e.Name,
				mode: nonemptyMode(e),
				path: "(no source)",
			})
			continue
		}
		canonical := e.Canonical
		if canonical == "" {
			canonical = e.Name
		}
		ent := exportEntry{
			RepoURL: e.Source,
			Skill:   canonical,
			Version: e.Ref,
		}
		if exportFrozen && e.Commit != "" {
			ent.Commit = e.Commit
		}
		if len(e.Targets) > 0 {
			ent.Targets = append(ent.Targets, e.Targets...)
			sort.Strings(ent.Targets)
		}
		// Alias is the local lock name when it differs from the canonical
		// skill name — i.e. when the user installed `qvr add foo --as bar`.
		// Carry it through so import recreates the same lock key.
		if e.Canonical != "" && e.Canonical != e.Name {
			ent.Alias = e.Name
		}
		if exportIncludeAliases && e.Registry != "" {
			ent.RegistryAlias = e.Registry
		}
		entries = append(entries, ent)
	}
	return entries, excluded, excludedDetails
}

func toManifestEntries(in []exportEntry) []manifest.Entry {
	out := make([]manifest.Entry, 0, len(in))
	for _, e := range in {
		out = append(out, manifest.Entry{
			RepoURL:       e.RepoURL,
			Skill:         e.Skill,
			Version:       e.Version,
			Commit:        e.Commit,
			Targets:       e.Targets,
			Alias:         e.Alias,
			RegistryAlias: e.RegistryAlias,
		})
	}
	return out
}

func toJSONEntries(in []exportEntry) []exportEntry {
	if in == nil {
		return []exportEntry{}
	}
	return in
}

func nonemptyMode(e *model.LockEntry) string {
	if e.Mode == "" {
		return "shared"
	}
	return e.Mode
}

func nonemptySource(e *model.LockEntry) string {
	if e.Source == "" {
		return "(unset)"
	}
	return e.Source
}
