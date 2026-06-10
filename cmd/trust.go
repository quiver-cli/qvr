package cmd

import (
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/output"
	"github.com/astra-sh/qvr/internal/skill"
	"github.com/spf13/cobra"
)

var (
	trustGlobal bool
	trustAll    bool
)

var trustCmd = &cobra.Command{
	Use:   "trust",
	Short: "Manage registry commit-author trust policy",
	RunE:  rejectUnknownSubcommand,
}

var trustListCmd = &cobra.Command{
	Use:   "list",
	Short: "List trusted commit authors by registry",
	Args:  cobra.NoArgs,
	RunE:  runTrustList,
}

var trustPinCmd = &cobra.Command{
	Use:   "pin <registry> <author>",
	Short: "Trust a commit author for one registry",
	Args:  cobra.ExactArgs(2),
	RunE:  runTrustPin,
}

var trustUnpinCmd = &cobra.Command{
	Use:   "unpin <registry> [author]",
	Short: "Remove a registry's author pins (or one author)",
	Args:  cobra.RangeArgs(1, 2),
	RunE:  runTrustUnpin,
}

var trustVerifyCmd = &cobra.Command{
	Use:   "verify [skill]",
	Short: "Verify installed skills against registry author policy",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runTrustVerify,
}

func init() {
	trustVerifyCmd.Flags().BoolVar(&trustGlobal, "global", false,
		"verify the user-global lock instead of the project lock")
	trustVerifyCmd.Flags().BoolVar(&trustAll, "all", false,
		"verify both project and global locks")
	trustCmd.AddCommand(trustListCmd, trustPinCmd, trustUnpinCmd, trustVerifyCmd)
	rootCmd.AddCommand(trustCmd)
}

func runTrustList(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	policy := cfg.Trust.Registries
	if policy == nil {
		policy = map[string]config.RegistryTrustConfig{}
	}
	if printer.Format == output.FormatJSON {
		return printer.JSON(policy)
	}
	if len(policy) == 0 {
		printer.Info("No registry author pins configured")
		return nil
	}
	names := make([]string, 0, len(policy))
	for name := range policy {
		names = append(names, name)
	}
	sort.Strings(names)
	rows := make([][]string, 0, len(names))
	for _, name := range names {
		authors := trustedAuthorsForRegistry(cfg, name)
		sort.Strings(authors)
		rows = append(rows, []string{name, strings.Join(authors, ", ")})
	}
	printer.Table([]string{"REGISTRY", "TRUSTED AUTHORS"}, rows)
	return nil
}

func runTrustPin(cmd *cobra.Command, args []string) error {
	registryName := strings.TrimSpace(args[0])
	author := strings.TrimSpace(args[1])
	if registryName == "" {
		return fmt.Errorf("registry must not be empty")
	}
	if author == "" {
		return fmt.Errorf("author must not be empty")
	}
	// A pin is matched against a git commit author (`Name <email>`), by full
	// identity or by email. A bare GitHub handle or name carries no email and
	// can never match — recording it would gate every install while reporting
	// success, so reject it up front rather than return a misleading ✓. #172.
	if !skill.ValidAuthorPin(author) {
		return fmt.Errorf("author %q is not a git commit identity: pin a commit-author email "+
			"(e.g. \"alice@example.com\") or a full \"Name <email>\" identity — GitHub handles are not resolved", author)
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if cfg.Trust.Registries == nil {
		cfg.Trust.Registries = map[string]config.RegistryTrustConfig{}
	}
	p := cfg.Trust.Registries[registryName]
	if slices.Contains(p.Authors, author) {
		printer.Success(fmt.Sprintf("Trusted author %q for registry %s", author, registryName))
		return config.Save(cfg)
	}
	p.Authors = append(p.Authors, author)
	sort.Strings(p.Authors)
	cfg.Trust.Registries[registryName] = p
	if err := config.Save(cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	printer.Success(fmt.Sprintf("Trusted author %q for registry %s", author, registryName))
	return nil
}

func runTrustUnpin(cmd *cobra.Command, args []string) error {
	registryName := strings.TrimSpace(args[0])
	if registryName == "" {
		return fmt.Errorf("registry must not be empty")
	}
	author := ""
	if len(args) > 1 {
		author = strings.TrimSpace(args[1])
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if cfg.Trust.Registries == nil {
		return fmt.Errorf("registry %s has no author pins", registryName)
	}
	p, ok := cfg.Trust.Registries[registryName]
	if !ok {
		return fmt.Errorf("registry %s has no author pins", registryName)
	}

	// No author given: drop the whole registry's pins.
	if author == "" {
		delete(cfg.Trust.Registries, registryName)
		if err := config.Save(cfg); err != nil {
			return fmt.Errorf("save config: %w", err)
		}
		printer.Success(fmt.Sprintf("Removed all author pins for registry %s", registryName))
		return nil
	}

	// Remove just the named author, matching by full identity.
	kept := p.Authors[:0:0]
	found := false
	for _, a := range p.Authors {
		if a == author {
			found = true
			continue
		}
		kept = append(kept, a)
	}
	if !found {
		return fmt.Errorf("author %q is not pinned for registry %s", author, registryName)
	}
	if len(kept) == 0 && len(p.Signers) == 0 {
		delete(cfg.Trust.Registries, registryName)
	} else {
		p.Authors = kept
		cfg.Trust.Registries[registryName] = p
	}
	if err := config.Save(cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	printer.Success(fmt.Sprintf("Removed author %q from registry %s", author, registryName))
	return nil
}

type trustVerifyRow struct {
	Skill    string `json:"skill"`
	Registry string `json:"registry,omitempty"`
	Author   string `json:"author,omitempty"`
	Status   string `json:"status"`
	Reason   string `json:"reason,omitempty"`
}

func runTrustVerify(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}
	locks, err := loadScopedLocks(projectRoot, trustGlobal, trustAll)
	if err != nil {
		return err
	}
	filter := ""
	if len(args) > 0 {
		filter = args[0]
	}
	rows, failed := collectTrustVerifyRows(locks, cfg, filter)
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Registry == rows[j].Registry {
			return rows[i].Skill < rows[j].Skill
		}
		return rows[i].Registry < rows[j].Registry
	})
	if printer.Format == output.FormatJSON {
		if err := printer.JSON(map[string]any{"results": rows, "failed": failed}); err != nil {
			return err
		}
		if failed > 0 {
			return errJSONHandled
		}
		return nil
	}
	if len(rows) == 0 {
		return fmt.Errorf("no installed skills matched %q", filter)
	}
	table := make([][]string, 0, len(rows))
	for _, row := range rows {
		table = append(table, []string{row.Skill, row.Registry, row.Author, row.Status, row.Reason})
	}
	printer.Table([]string{"SKILL", "REGISTRY", "AUTHOR", "STATUS", "REASON"}, table)
	if failed > 0 {
		return fmt.Errorf("trust verify failed for %s", output.Plural(failed, "skill"))
	}
	return nil
}

// collectTrustVerifyRows verifies every (optionally filtered) installed skill
// across the scoped locks, returning the rows and the count with status
// "failed".
func collectTrustVerifyRows(locks []scopedLock, cfg *config.Config, filter string) ([]trustVerifyRow, int) {
	var rows []trustVerifyRow
	failed := 0
	for _, scoped := range locks {
		for _, entry := range scoped.Lock.Entries() {
			if filter != "" && entry.Name != filter {
				continue
			}
			row := verifyTrustEntry(entry, cfg)
			rows = append(rows, row)
			if row.Status == "failed" {
				failed++
			}
		}
	}
	return rows, failed
}

func verifyTrustEntry(entry *model.LockEntry, cfg *config.Config) trustVerifyRow {
	row := trustVerifyRow{
		Skill:    entry.Name,
		Registry: entry.Registry,
		Status:   "unconfigured",
		Reason:   "no author pins for registry",
	}
	if entry.IsLink() {
		row.Status = "skipped"
		row.Reason = "local link install"
		return row
	}
	authors := trustedAuthorsForRegistry(cfg, entry.Registry)
	if len(authors) == 0 {
		return row
	}
	author := entry.AuthorIdentity()
	row.Author = author
	switch {
	case author == "":
		row.Status = "failed"
		row.Reason = "commit author not recorded"
	case skill.AuthorAllowed(author, authors):
		row.Status = "trusted"
		row.Reason = "author pinned"
	default:
		row.Status = "failed"
		row.Reason = "author not pinned"
	}
	return row
}

func trustedAuthorsForRegistry(cfg *config.Config, registryName string) []string {
	if cfg == nil || cfg.Trust.Registries == nil || registryName == "" {
		return nil
	}
	p := cfg.Trust.Registries[registryName]
	if len(p.Authors) > 0 {
		return append([]string{}, p.Authors...)
	}
	return append([]string{}, p.Signers...)
}

func trustedAuthorsByRegistry(cfg *config.Config) map[string][]string {
	out := map[string][]string{}
	if cfg == nil || cfg.Trust.Registries == nil {
		return out
	}
	for name := range cfg.Trust.Registries {
		if authors := trustedAuthorsForRegistry(cfg, name); len(authors) > 0 {
			out[name] = authors
		}
	}
	return out
}
