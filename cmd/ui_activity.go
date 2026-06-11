package cmd

import (
	"fmt"
	"net/http"
	"sync"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/ops"
	"github.com/astra-sh/qvr/internal/ops/discover"
	"github.com/astra-sh/qvr/internal/ops/store"
)

// The activity analytics endpoint + the discover trigger behind the
// dashboard's overview widgets and refresh button.
//
// Degradation contract matches the other metrics endpoints: a nil store or a
// not-yet-migrated DB answers audit_enabled:false with empty data, never a 500.

// activityResponse is GET /api/metrics/activity — the analytics overview
// payload: headline totals (sessions/turns/tokens/duration), the per-day
// per-agent series, and the scan-skipped (skill-less, unstored) series that
// gives the skill-vs-no-skill dimension without storing those transcripts.
type activityResponse struct {
	AuditEnabled bool                    `json:"audit_enabled"`
	Scope        string                  `json:"scope"`
	Summary      *store.ActivitySummary  `json:"summary"`
	Series       []*store.ActivityBucket `json:"series"`
	// Skipped is machine-global by nature (the scan ledger has no project
	// scoping); it is omitted in project scope rather than mislabeled.
	Skipped []*store.SkippedBucket `json:"skipped,omitempty"`
}

func (s *uiServer) handleActivity(w http.ResponseWriter, r *http.Request) {
	sc, err := s.resolveScope(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	resp := activityResponse{
		Scope:   sc.label(),
		Summary: &store.ActivitySummary{Agents: []*store.AgentActivity{}},
		Series:  []*store.ActivityBucket{},
	}
	if s.store == nil {
		writeJSON(w, http.StatusOK, resp)
		return
	}

	q := r.URL.Query()
	f := &store.ActivityFilter{
		Dirs:  sc.auditDirs(),
		Since: parseDateParam(q.Get("since"), false),
		Until: parseDateParam(q.Get("until"), true),
	}
	if agent := canonicalAgentFlag(q.Get("agent")); agent != "" {
		f.Agents = []string{agent}
	}

	ctx := r.Context()
	summary, err := s.store.ActivitySummary(ctx, f)
	if err != nil {
		if schemaNotReady(err) {
			writeJSON(w, http.StatusOK, resp)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	resp.AuditEnabled = true
	resp.Summary = summary
	series, err := s.store.ActivitySeries(ctx, f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if series != nil {
		resp.Series = series
	}
	// The skipped series is global-only: project dirs can't scope the ledger.
	if len(f.Dirs) == 0 {
		skipped, kerr := s.store.SkippedSkilllessSeries(ctx, f.Since, f.Until)
		if kerr != nil {
			writeErr(w, http.StatusInternalServerError, kerr)
			return
		}
		resp.Skipped = skipped
	}
	writeJSON(w, http.StatusOK, resp)
}

// discoverScanMu serialises discovery scans within this process: a scan reads
// the ledger up front, so two concurrent scans would both decide the same
// files need ingesting and tail overlapping byte ranges into duplicate rows.
// Shared by the HTTP handler and the `qvr ui --discover` launch goroutine.
var discoverScanMu sync.Mutex

// handleDiscover is POST /api/discover — the dashboard's refresh/back-fill
// button. The server's own store handle is read-only, so the scan opens a
// short-lived read-write handle (SQLite WAL lets the read handle see the
// writer's commits as soon as they land).
func (s *uiServer) handleDiscover(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeErr(w, http.StatusConflict,
			fmt.Errorf("audit database not initialized — run `qvr audit discover` once (or relaunch with `qvr ui --discover`)"))
		return
	}
	if !discoverScanMu.TryLock() {
		writeErr(w, http.StatusConflict, fmt.Errorf("a discovery scan is already running"))
		return
	}
	defer discoverScanMu.Unlock()

	cfg, err := config.Load()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	rw, err := store.Open(r.Context(), store.OpenOptions{Path: ops.DBPath(cfg)})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("open audit store: %w", err))
		return
	}
	defer func() { _ = rw.Close() }()

	rep, err := discover.Scan(r.Context(), rw, discover.Options{})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("discover: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, rep)
}
