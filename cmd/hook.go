package cmd

import (
	"fmt"
	"io"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/ops"
	"github.com/astra-sh/qvr/internal/ops/rawtrace"
	"github.com/astra-sh/qvr/internal/ops/store"
	"github.com/astra-sh/qvr/internal/output"
	"github.com/spf13/cobra"
)

var hookCmd = &cobra.Command{
	Use:    "_hook <agent> <hook_type>",
	Short:  "Ingest a hook event (internal use)",
	Hidden: true,
	Args:   cobra.ExactArgs(2),
	RunE:   runHook,
}

func init() {
	rootCmd.AddCommand(hookCmd)
}

// runHook is the entry point for `qvr _hook <agent> <hook_type>`.
//
// A hook firing is a trigger, not the data: runHook reads the hook payload from
// stdin and hands it to the raw-trace capturer, which tails the agent's own
// transcript for new lines and stores both those lines and the payload verbatim
// (zero parsing/normalization), then re-derives the session's spans. Capture is
// a silent no-op when ops is disabled in config — exit 0, no error, no DB.
func runHook(cmd *cobra.Command, args []string) error {
	agent := args[0]
	hookType := args[1]

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if !ops.Enabled(cfg) {
		return nil // silent no-op — the documented disabled behaviour
	}

	raw, err := io.ReadAll(cmd.InOrStdin())
	if err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}
	// Empty stdin is normal for some agents (e.g. Codex in `codex exec` mode
	// fires the hook without piping a payload). We can still tail the transcript
	// via the session env override, so don't drop — just note it on stderr
	// (hooks discard stderr in normal operation; this only shows on a hand-run).
	if len(raw) == 0 {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"qvr _hook: %s %s received empty stdin (agent fired the hook but delivered no payload)\n",
			agent, hookType)
	}

	// Open the store; run migrations if this is the first _hook call
	// since `qvr audit enable`.
	s, err := store.Open(cmd.Context(), store.OpenOptions{Path: ops.DBPath(cfg)})
	if err != nil {
		return fmt.Errorf("open ops store: %w", err)
	}
	defer s.Close()

	res, err := rawtrace.Capture(cmd.Context(), s, agent, hookType, raw)
	if err != nil {
		// A capture failure must not break the agent's hook pipeline. Surface
		// it on stderr and exit 0.
		fmt.Fprintf(cmd.ErrOrStderr(), "qvr _hook: capture failed: %v\n", err)
		return nil
	}
	if res != nil && res.Pruned {
		// Skill-only retention dropped the completed session (no skill usage).
		fmt.Fprintf(cmd.ErrOrStderr(),
			"qvr _hook: %s %s dropped session (no skill usage)\n", agent, hookType)
	} else if res != nil && (res.LinesStored > 0 || res.HookStored) {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"qvr _hook: %s %s captured %s, %s, hook_payload=%t\n",
			agent, hookType, output.Plural(res.LinesStored, "line"),
			output.Plural(res.SpansStored, "span"), res.HookStored)
	}
	return nil
}
