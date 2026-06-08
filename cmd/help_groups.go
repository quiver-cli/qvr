package cmd

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// flagGroupAnnotationKey buckets flags into named sections rendered by
// groupedUsageFunc. Commands with many flags (publish — 17 of them,
// issue #109) get a Common / Authoring / Routing / Trust / Scope layout
// instead of one flat alphabetical wall. Flags without an annotation
// fall through to an "Other" section so adding a new flag without
// remembering to group it surfaces it in --help rather than hiding it.
const flagGroupAnnotationKey = "qvr.flag.group"

// Command-group IDs for the top-level `qvr --help` layout (issue #161). The
// everyday install / version / publish loop lives under "Primary"; authoring,
// maintenance, and introspection helpers — which are rarely run standalone and
// inflate a 30-plus command list — are demoted to "Advanced / Authoring".
const (
	groupPrimary  = "primary"
	groupAdvanced = "advanced"
)

// advancedCommands are demoted out of the primary help list. Each is either an
// authoring-time check (lint), a side effect of an everyday verb (docs runs
// inside sync), or a maintenance/introspection helper most users never invoke
// directly. Behavior is unchanged — only their placement in `qvr --help` moves.
var advancedCommands = map[string]bool{
	"lint":       true, // advisory spec check; also rides along with `qvr scan`
	"docs":       true, // runs as a side effect of `qvr sync`
	"doctor":     true,
	"provenance": true,
	"resolve":    true,
	"import":     true,
	"export":     true,
	"hook":       true,
	"audit":      true,
	"scan":       true,
	"ui":         true,
	"diff":       true,
	"tree":       true,
	"locks":      true,
	"version":    true,
}

// assignCommandGroups buckets every top-level command into the Primary or
// Advanced group so `qvr --help` reads as the ~dozen verbs that matter, with
// the rest tucked under an advanced heading instead of one flat wall (#161).
// Called from Execute() after every init() has run AddCommand, so the command
// set is complete.
func assignCommandGroups(root *cobra.Command) {
	root.AddGroup(
		&cobra.Group{ID: groupPrimary, Title: "Primary Commands:"},
		&cobra.Group{ID: groupAdvanced, Title: "Advanced / Authoring:"},
	)
	for _, c := range root.Commands() {
		switch c.Name() {
		case "help", "completion":
			c.GroupID = groupAdvanced
		default:
			if advancedCommands[c.Name()] {
				c.GroupID = groupAdvanced
			} else {
				c.GroupID = groupPrimary
			}
		}
	}
	// Cobra's auto-generated help/completion commands need an explicit group or
	// they render under a stray "Additional Commands" heading.
	root.SetHelpCommandGroupID(groupAdvanced)
	root.SetCompletionCommandGroupID(groupAdvanced)
}

// markFlagGroup tags a flag with a group name for grouped --help
// rendering. Safe to call before or after the flag is registered.
func markFlagGroup(fs *pflag.FlagSet, flagName, group string) {
	if err := fs.SetAnnotation(flagName, flagGroupAnnotationKey, []string{group}); err != nil {
		// Annotation set fails only when the flag isn't registered;
		// silently no-op so init() ordering bugs surface as flags
		// landing in "Other" rather than as panics on every invocation.
		_ = err
	}
}

// groupedUsageFunc returns a cobra UsageFunc that renders the command's
// local flags bucketed by their flagGroupAnnotationKey annotation, in
// the order given by `order`. Any flag without an annotation lands in
// an automatic trailing "Other" bucket so newly-added flags can't
// silently disappear.
//
// The rest of the help (usage line, aliases, examples, global flags)
// is rendered the same as cobra's default. The command's `Long`
// description is deliberately NOT printed here because cobra's default
// HelpTemplate already prints it before calling UsageString (issue #114
// — printing it again here caused --help to double the block).
func groupedUsageFunc(order []string) func(*cobra.Command) error {
	return func(c *cobra.Command) error {
		w := c.OutOrStderr()
		fmt.Fprintf(w, "Usage:\n  %s", c.UseLine())
		if c.HasAvailableSubCommands() {
			fmt.Fprintf(w, "\n  %s [command]", c.CommandPath())
		}
		fmt.Fprintln(w)
		if len(c.Aliases) > 0 {
			fmt.Fprintf(w, "\nAliases:\n  %s\n", c.NameAndAliases())
		}
		if c.HasExample() {
			fmt.Fprintf(w, "\nExamples:\n%s\n", c.Example)
		}

		// Bucket flags by annotation.
		buckets := map[string][]*pflag.Flag{}
		seenOrder := []string{}
		seen := map[string]bool{}
		c.LocalFlags().VisitAll(func(f *pflag.Flag) {
			bucket := "Other"
			if names, ok := f.Annotations[flagGroupAnnotationKey]; ok && len(names) > 0 {
				bucket = names[0]
			}
			if !seen[bucket] {
				seen[bucket] = true
				seenOrder = append(seenOrder, bucket)
			}
			buckets[bucket] = append(buckets[bucket], f)
		})

		// Render in the requested order, then any unlisted buckets, then "Other" last.
		emitted := map[string]bool{}
		emit := func(name string) {
			flags, ok := buckets[name]
			if !ok || emitted[name] {
				return
			}
			emitted[name] = true
			fmt.Fprintf(w, "\n%s:\n", name)
			renderFlagUsages(w, flags)
		}
		for _, name := range order {
			if name == "Other" {
				continue
			}
			emit(name)
		}
		for _, name := range seenOrder {
			if name == "Other" {
				continue
			}
			emit(name)
		}
		emit("Other")

		if c.HasAvailableInheritedFlags() {
			fmt.Fprintf(w, "\nGlobal Flags:\n%s", c.InheritedFlags().FlagUsages())
		}
		if c.HasAvailableSubCommands() {
			fmt.Fprintf(w, "\nUse \"%s [command] --help\" for more information about a command.\n", c.CommandPath())
		}
		return nil
	}
}

// renderFlagUsages writes pflag's standard per-flag rendering for a
// subset of flags. We can't call FlagSet.FlagUsages on a sub-set
// directly because pflag's renderer iterates the whole set; so we
// build a temporary FlagSet and let pflag format it.
func renderFlagUsages(w io.Writer, flags []*pflag.Flag) {
	tmp := pflag.NewFlagSet("group", pflag.ContinueOnError)
	for _, f := range flags {
		tmp.AddFlag(f)
	}
	fmt.Fprint(w, tmp.FlagUsages())
}
