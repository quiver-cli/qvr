package cmd

import (
	"fmt"
	"time"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/ops/derive"
	"github.com/astra-sh/qvr/internal/ops/rawtrace"
	"github.com/astra-sh/qvr/internal/ops/store"
	"github.com/astra-sh/qvr/internal/output"
	"github.com/spf13/cobra"
)

var gcOlderThan string

// defaultRawRetention is how far back `qvr audit gc` keeps raw traces when no
// --older-than is given.
const defaultRawRetention = 30 * 24 * time.Hour

// skilllessGrace spares very recent sessions from the skill-only sweep: a
// session whose last activity is within this window may still be in progress
// (a skill could yet be used), so gc leaves it for the completion-hook gate.
const skilllessGrace = time.Hour

var auditGCCmd = &cobra.Command{
	Use:    "gc",
	Hidden: true, // maintenance plumbing — see `qvr audit --help`
	Short:  "Prune captured raw traces older than a cutoff",
	Long: `Delete raw trace rows (and their derived spans become stale) captured
before a cutoff, to bound the local database size. Defaults to pruning anything
older than 30 days; override with --older-than.`,
	Args: cobra.NoArgs,
	RunE: runAuditGC,
}

func init() {
	auditGCCmd.Flags().StringVar(&gcOlderThan, "older-than", "",
		"prune traces captured before this age (e.g. 24h, 7d; default 30d)")
	auditCmd.AddCommand(auditGCCmd)
}

func runAuditGC(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	cutoff := time.Now().UTC().Add(-defaultRawRetention)
	if gcOlderThan != "" {
		t, perr := parseTimeFlag(gcOlderThan)
		if perr != nil {
			return fmt.Errorf("invalid --older-than: %w", perr)
		}
		cutoff = t
	}

	if !auditDBExists(cfg) {
		if outputFormat == "json" {
			return printer.JSON(map[string]any{"traces_pruned": 0})
		}
		printer.Info("Nothing to prune")
		return nil
	}

	s, err := openAuditStore(cmd.Context(), cfg, false)
	if err != nil {
		return fmt.Errorf("open audit store: %w", err)
	}
	defer s.Close()

	n, err := s.DeleteRawBefore(cmd.Context(), cutoff)
	if err != nil {
		return fmt.Errorf("prune raw traces: %w", err)
	}

	// Skill-only retention backstop: drop sessions that completed with no skill
	// usage but never got a clean completion-hook prune (e.g. the agent crashed
	// or the Stop hook never fired). Mirrors the gate in rawtrace.Capture.
	sessionsPruned, err := sweepSkilllessSessions(cmd, s)
	if err != nil {
		return err
	}

	if outputFormat == "json" {
		return printer.JSON(map[string]any{
			"traces_pruned":   n,
			"sessions_pruned": sessionsPruned,
		})
	}
	printer.Success(fmt.Sprintf("Pruned %s", output.Plural(int(n), "raw trace")))
	if sessionsPruned > 0 {
		printer.Info(fmt.Sprintf("Dropped %s", output.Plural(sessionsPruned, "skill-less session")))
	}
	return nil
}

// sweepSkilllessSessions re-derives each settled session and deletes the ones
// with no skill usage. Only sessions whose agent has a deriver are eligible
// (skill absence is unprovable otherwise), and only those past skilllessGrace
// (recent ones may still be active — left to the completion-hook gate).
func sweepSkilllessSessions(cmd *cobra.Command, s store.Store) (int, error) {
	sessions, err := s.ListRawSessions(cmd.Context(), &store.RawSessionFilter{})
	if err != nil {
		return 0, fmt.Errorf("list sessions: %w", err)
	}
	cutoff := time.Now().UTC().Add(-skilllessGrace)
	pruned := 0
	for _, sess := range sessions {
		if _, ok := derive.Get(sess.AgentName); !ok {
			continue // can't evaluate skill usage for this agent — keep it
		}
		if sess.LastAt.After(cutoff) {
			continue // too recent — may still be in progress
		}
		_, hasSkill, derr := rawtrace.Rederive(cmd.Context(), s, sess.SessionID, sess.AgentName)
		if derr != nil || hasSkill {
			continue
		}
		if _, derr := s.DeleteSession(cmd.Context(), sess.SessionID); derr == nil {
			pruned++
		}
	}
	return pruned, nil
}
