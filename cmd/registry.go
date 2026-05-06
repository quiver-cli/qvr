package cmd

import (
	"fmt"
	"time"

	"github.com/raks097/quiver/internal/git"
	"github.com/raks097/quiver/internal/output"
	"github.com/raks097/quiver/internal/registry"
	"github.com/spf13/cobra"
)

var registryCmd = &cobra.Command{
	Use:   "registry",
	Short: "Manage skill registries",
}

var registryAddCmd = &cobra.Command{
	Use:   "add <name> <url>",
	Short: "Add a Git repository as a skill registry",
	Args:  cobra.ExactArgs(2),
	RunE:  runRegistryAdd,
}

var registryRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove a registry and its cached data",
	Args:  cobra.ExactArgs(1),
	RunE:  runRegistryRemove,
}

var registryListCmd = &cobra.Command{
	Use:   "list [name...]",
	Short: "List configured registries, or skills within named registries",
	Long: `List all configured registries when called with no arguments.
When one or more registry names are given, list the skills contained in
each of those registries.`,
	Args: cobra.ArbitraryArgs,
	RunE: runRegistryList,
}

var (
	registryUpdateCheck   bool
	registryUpdateVerbose bool
	registryListFull      bool
)

var registryUpdateCmd = &cobra.Command{
	Use:   "update [name]",
	Short: "Fetch latest changes from registries",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runRegistryUpdate,
}

func init() {
	registryUpdateCmd.Flags().BoolVar(&registryUpdateCheck, "check", false,
		"check for upstream changes without downloading")
	registryUpdateCmd.Flags().BoolVarP(&registryUpdateVerbose, "verbose", "v", false,
		"print per-skill skip reasons when any skills could not be indexed")
	registryListCmd.Flags().BoolVar(&registryListFull, "full", false,
		"print full descriptions without truncation")
	registryCmd.AddCommand(registryAddCmd, registryRemoveCmd, registryListCmd, registryUpdateCmd)
	rootCmd.AddCommand(registryCmd)
}

func runRegistryAdd(cmd *cobra.Command, args []string) error {
	name, url := args[0], args[1]
	mgr := registry.NewManager(git.NewGoGitClient())

	reg, err := mgr.Add(cmd.Context(), name, url)
	if err != nil {
		return fmt.Errorf("add registry: %w", err)
	}

	if printer.Format == output.FormatJSON {
		return printer.JSON(reg)
	}
	if reg.CredentialsStripped {
		printer.Warning("URL contained embedded credentials; stored sanitised URL. " +
			"Configure a credential helper (e.g. `gh auth login` or osxkeychain) for auth.")
	}
	msg := fmt.Sprintf("Added registry %q (%s) with %d skills", reg.Name, reg.URL, reg.SkillCount)
	if reg.SkippedCount > 0 {
		msg += fmt.Sprintf(" (%d skipped — run `qvr registry update %s --verbose` for reasons)",
			reg.SkippedCount, reg.Name)
	}
	printer.Success(msg)
	return nil
}

func runRegistryRemove(cmd *cobra.Command, args []string) error {
	mgr := registry.NewManager(git.NewGoGitClient())
	if err := mgr.Remove(args[0]); err != nil {
		return fmt.Errorf("remove registry: %w", err)
	}

	if printer.Format == output.FormatJSON {
		return printer.JSON(map[string]string{"removed": args[0]})
	}
	printer.Success(fmt.Sprintf("Removed registry %q", args[0]))
	return nil
}

func runRegistryList(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return runRegistrySkillsList(args)
	}

	mgr := registry.NewManager(git.NewGoGitClient())
	registries, err := mgr.List()
	if err != nil {
		return fmt.Errorf("list registries: %w", err)
	}

	if printer.Format == output.FormatJSON {
		return printer.JSON(registries)
	}

	if len(registries) == 0 {
		printer.Info("No registries configured. Run 'qvr registry add <name> <url>' to add one.")
		return nil
	}

	// Only render the SKIPPED column when at least one registry has malformed
	// skills — the common case stays uncluttered.
	anySkipped := false
	for _, r := range registries {
		if r.SkippedCount > 0 {
			anySkipped = true
			break
		}
	}

	headers := []string{"NAME", "URL", "SKILLS", "LAST FETCHED"}
	if anySkipped {
		headers = []string{"NAME", "URL", "SKILLS", "SKIPPED", "LAST FETCHED"}
	}
	var rows [][]string
	for _, r := range registries {
		fetched := "never"
		if !r.LastFetched.IsZero() {
			fetched = time.Since(r.LastFetched).Truncate(time.Second).String() + " ago"
		}
		row := []string{r.Name, r.URL, fmt.Sprintf("%d", r.SkillCount)}
		if anySkipped {
			row = append(row, fmt.Sprintf("%d", r.SkippedCount))
		}
		row = append(row, fetched)
		rows = append(rows, row)
	}
	printer.Table(headers, rows)
	if anySkipped {
		printer.Info("Some skills could not be indexed. Run `qvr registry update <name> --verbose` for reasons.")
	}
	return nil
}

func runRegistryUpdate(cmd *cobra.Command, args []string) error {
	mgr := registry.NewManager(git.NewGoGitClient())
	name := ""
	if len(args) > 0 {
		name = args[0]
	}

	if registryUpdateCheck {
		results, err := mgr.Check(cmd.Context(), name)
		if err != nil {
			return fmt.Errorf("check registries: %w", err)
		}
		if printer.Format == output.FormatJSON {
			return printer.JSON(results)
		}
		for _, r := range results {
			if r.Error != "" {
				printer.Error(fmt.Sprintf("%s: %s", r.Name, r.Error))
			} else if r.HasUpstreamChanges {
				printer.Info(fmt.Sprintf("%s: upstream changes available", r.Name))
			} else {
				printer.Info(fmt.Sprintf("%s: up to date", r.Name))
			}
		}
		return nil
	}

	results, err := mgr.Update(cmd.Context(), name)
	if err != nil {
		return fmt.Errorf("update registries: %w", err)
	}

	if printer.Format == output.FormatJSON {
		return printer.JSON(results)
	}
	for _, r := range results {
		if r.Error != "" {
			printer.Error(fmt.Sprintf("%s: %s", r.Name, r.Error))
			continue
		}
		msg := fmt.Sprintf("%s: updated (%d skills", r.Name, r.SkillCount)
		if r.SkippedCount > 0 {
			msg += fmt.Sprintf(", %d skipped", r.SkippedCount)
		}
		msg += ")"
		printer.Success(msg)
		if registryUpdateVerbose && len(r.Skipped) > 0 {
			for _, s := range r.Skipped {
				printer.Warning(fmt.Sprintf("  skipped %s (%s): %s", s.Name, s.Path, s.Reason))
			}
		}
	}
	return nil
}

func runRegistrySkillsList(names []string) error {
	mgr := registry.NewManager(git.NewGoGitClient())
	results, err := mgr.ListSkills(names)
	if err != nil {
		return fmt.Errorf("list skills: %w", err)
	}

	if printer.Format == output.FormatJSON {
		return printer.JSON(results)
	}

	showRegCol := len(names) > 1
	headers := []string{"SKILL", "DESCRIPTION"}
	if showRegCol {
		headers = []string{"REGISTRY", "SKILL", "DESCRIPTION"}
	}

	var rows [][]string
	errored := 0
	for _, r := range results {
		if r.Error != "" {
			printer.Error(fmt.Sprintf("%s: %s", r.Name, r.Error))
			errored++
			continue
		}
		if len(r.Skills) == 0 {
			printer.Info(fmt.Sprintf("%s: no skills", r.Name))
			continue
		}
		for _, s := range r.Skills {
			desc := output.TruncDesc(s.Description, registryListFull)
			if showRegCol {
				rows = append(rows, []string{r.Name, s.Name, desc})
			} else {
				rows = append(rows, []string{s.Name, desc})
			}
		}
	}

	if len(rows) > 0 {
		printer.Table(headers, rows)
	}
	if errored == len(results) {
		return fmt.Errorf("no valid registries provided")
	}
	return nil
}
