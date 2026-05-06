package cmd

import (
	"fmt"
	"os"

	"github.com/raks097/quiver/internal/git"
	"github.com/raks097/quiver/internal/output"
	"github.com/raks097/quiver/internal/skill"
	"github.com/spf13/cobra"
)

var (
	publishRegistry       string
	publishBranch         string
	publishTag            string
	publishMessage        string
	publishAuthor         string
	publishEmail          string
	publishDryRun         bool
	publishNoCreateBranch bool
)

var publishCmd = &cobra.Command{
	Use:   "publish [path]",
	Short: "Publish a local skill to a registry",
	Long: `Clone the target registry into a temp directory, copy the local skill into
skills/<name>/, commit and push. Validates the skill before touching the registry.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runPublish,
}

func init() {
	publishCmd.Flags().StringVar(&publishRegistry, "registry", "", "target registry (defaults to default_registry config)")
	publishCmd.Flags().StringVar(&publishBranch, "branch", "", "target branch (defaults to registry default)")
	publishCmd.Flags().StringVar(&publishTag, "tag", "", "annotated tag to create on the new commit (e.g. v1.2.0)")
	publishCmd.Flags().StringVarP(&publishMessage, "message", "m", "", "commit message")
	publishCmd.Flags().StringVar(&publishAuthor, "author", "", "commit author name")
	publishCmd.Flags().StringVar(&publishEmail, "email", "", "commit author email")
	publishCmd.Flags().BoolVar(&publishDryRun, "dry-run", false, "validate and stage without pushing")
	publishCmd.Flags().BoolVar(&publishNoCreateBranch, "no-create-branch", false, "refuse to create --branch if it doesn't already exist on origin")
	rootCmd.AddCommand(publishCmd)
}

func runPublish(cmd *cobra.Command, args []string) error {
	path := "."
	if len(args) == 1 {
		path = args[0]
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}

	p := skill.NewPublisher(git.NewGoGitClient())
	result, err := p.Publish(cmd.Context(), skill.PublishRequest{
		LocalPath:      path,
		Registry:       publishRegistry,
		Branch:         publishBranch,
		Tag:            publishTag,
		Message:        publishMessage,
		Author:         publishAuthor,
		AuthorEmail:    publishEmail,
		DryRun:         publishDryRun,
		NoCreateBranch: publishNoCreateBranch,
	})
	if err != nil {
		return fmt.Errorf("publish: %w", err)
	}
	if printer.Format == output.FormatJSON {
		return printer.JSON(result)
	}
	if result.DryRun {
		tagSuffix := ""
		if result.Tag != "" {
			tagSuffix = fmt.Sprintf(" (tag %s)", result.Tag)
		}
		printer.Info(fmt.Sprintf("Dry run OK: %s would be published to %s@%s%s", result.Skill, result.Registry, result.Branch, tagSuffix))
		return nil
	}
	shortCommit := result.Commit
	if len(shortCommit) >= 7 {
		shortCommit = shortCommit[:7]
	} else if shortCommit == "" {
		shortCommit = "<unknown>"
	}
	msg := fmt.Sprintf("Published %s to %s@%s (%s)", result.Skill, result.Registry, result.Branch, shortCommit)
	if result.Tag != "" {
		msg += fmt.Sprintf(", tagged %s", result.Tag)
	}
	printer.Success(msg)
	return nil
}
