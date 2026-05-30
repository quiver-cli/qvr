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
// The rest of the help (usage line, aliases, examples, long
// description, global flags) is rendered the same as cobra's default.
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
		if c.Long != "" {
			fmt.Fprintf(w, "\n%s\n", c.Long)
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
