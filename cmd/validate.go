package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/raks097/quiver/internal/output"
	"github.com/raks097/quiver/internal/skill"
	"github.com/spf13/cobra"
)

var (
	validateGlobal bool
)

var validateCmd = &cobra.Command{
	Use:   "validate [path-or-name]",
	Short: "Validate a skill's SKILL.md and directory structure",
	Long: `Checks that SKILL.md frontmatter conforms to the agentskills.io
specification.

The argument can be either a filesystem path (` + "`.`" + `, ` + "`./demo`" + `,
` + "`/abs/path`" + `, etc.) or the name of an installed skill — when bare
(no path separators), the name is resolved through the project's lock
file (or the global lock when --global is set), so ` + "`qvr validate demo`" + `
just works after ` + "`qvr add demo`" + `. This mirrors ` + "`qvr scan`" + `'s
resolution behaviour (issue #64). When the path is a registry root
(skills/<name>/SKILL.md layout), the skill directory is auto-discovered.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runValidate,
}

func init() {
	validateCmd.Flags().BoolVar(&validateGlobal, "global", false,
		"resolve a bare skill name through the user-global lock instead of the project lock")
	rootCmd.AddCommand(validateCmd)
}

func runValidate(cmd *cobra.Command, args []string) error {
	dir := "."
	if len(args) > 0 {
		dir = args[0]
	}

	resolved, discovered, err := resolveSkillArg(dir, validateGlobal)
	if err != nil {
		if printer.Format == output.FormatJSON {
			_ = printer.JSON(map[string]any{
				"valid":      false,
				"path":       dir,
				"discovered": discovered,
				"errors":     []map[string]string{{"message": err.Error()}},
			})
			return errJSONHandled
		}
		if len(discovered) > 1 {
			printer.Error(err.Error())
			for _, d := range discovered {
				fmt.Fprintf(printer.Out, "  %s\n", d)
			}
			return fmt.Errorf("ambiguous skill path")
		}
		return err
	}

	if resolved != dir {
		printer.Info(fmt.Sprintf("discovered skill at %s", resolved))
	}

	s, err := skill.LoadFromPath(resolved)
	if err != nil {
		if printer.Format == output.FormatJSON {
			_ = printer.JSON(map[string]any{
				"valid":  false,
				"path":   resolved,
				"errors": []map[string]string{{"message": err.Error()}},
			})
			return errJSONHandled
		}
		return fmt.Errorf("load skill: %w", err)
	}

	result := skill.Validate(s)

	if printer.Format == output.FormatJSON {
		if err := printer.JSON(result); err != nil {
			return err
		}
		if !result.Valid {
			return errJSONHandled
		}
		return nil
	}

	if result.Valid {
		printer.Success(fmt.Sprintf("Skill %q is valid", s.Frontmatter.Name))
		return nil
	}

	printer.Error(fmt.Sprintf("Skill at %s has %d issue(s):", resolved, len(result.Errors)))
	for _, e := range result.Errors {
		fmt.Printf("  [%s] %s: %s\n", e.Severity, e.Field, e.Message)
	}
	return fmt.Errorf("validation failed with %d error(s)", len(result.Errors))
}

// resolveSkillDir handles the registry-layout case: when the given path doesn't
// itself contain SKILL.md, we look for skills/*/SKILL.md.
//   - exactly one match → return its dir.
//   - multiple matches → return an error and the list, so the caller can show
//     them all and ask the user to pick one.
//   - zero matches → return a single error explaining what was checked.
//
// When the given path already contains SKILL.md, returns it unchanged.
func resolveSkillDir(dir string) (string, []string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", nil, fmt.Errorf("resolve path: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", nil, fmt.Errorf("stat path: %w", err)
	}
	if !info.IsDir() {
		return "", nil, fmt.Errorf("%s is not a directory", abs)
	}
	if _, err := os.Stat(filepath.Join(abs, "SKILL.md")); err == nil {
		return abs, nil, nil
	}

	matches, err := filepath.Glob(filepath.Join(abs, "skills", "*", "SKILL.md"))
	if err != nil {
		return "", nil, fmt.Errorf("scan skills/: %w", err)
	}
	sort.Strings(matches)
	dirs := make([]string, 0, len(matches))
	for _, m := range matches {
		dirs = append(dirs, filepath.Dir(m))
	}

	switch len(dirs) {
	case 0:
		hint := fmt.Sprintf("SKILL.md not found at %s; "+
			"is this a registry root? try passing a specific skill dir, "+
			"e.g. %s/skills/<name>", abs, abs)
		return "", nil, fmt.Errorf("%s", hint)
	case 1:
		return dirs[0], dirs, nil
	default:
		rels := make([]string, len(dirs))
		for i, d := range dirs {
			if rel, err := filepath.Rel(abs, d); err == nil {
				rels[i] = rel
			} else {
				rels[i] = d
			}
		}
		return "", dirs, fmt.Errorf(
			"found %d skills under %s/skills/; pass one explicitly: %s",
			len(dirs), abs, strings.Join(rels, ", "),
		)
	}
}
