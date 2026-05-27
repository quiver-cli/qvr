package security

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// VulnProvider abstracts vulnerability lookup so the dependency check
// can run against either the embedded offline DB or an online source
// (osv.dev) without coupling the check to a transport.
//
// Implementations must be safe for concurrent use. Lookup may return
// an empty slice when the provider has no opinion on the dependency.
type VulnProvider interface {
	Lookup(ctx context.Context, dep Dependency) ([]VulnRecord, error)
}

// OfflineProvider scans the package-shipped vulnerability list. It is
// the default provider — no network, deterministic in CI.
type OfflineProvider struct{}

// Lookup walks the offline DB. Errors are not returned — the offline
// path is infallible.
func (OfflineProvider) Lookup(_ context.Context, dep Dependency) ([]VulnRecord, error) {
	var hits []VulnRecord
	for _, v := range offlineVulnDB {
		if v.Ecosystem != dep.Ecosystem || v.Name != dep.Name {
			continue
		}
		if dep.Version == "" || versionLE(dep.Version, v.MaxAffected) {
			hits = append(hits, v)
		}
	}
	return hits, nil
}

// OSVProvider queries https://api.osv.dev/v1/querybatch for online
// vulnerability data. It maintains a small in-memory TTL cache so
// repeated scans of the same skill don't re-query the same package.
//
// The provider is deliberately conservative: on any error (network,
// HTTP, parse), it returns nil instead of failing the scan. The
// dependency check then falls back to the offline DB. This keeps `qvr
// scan` usable on locked-down build hosts.
type OSVProvider struct {
	client *http.Client
	url    string
	ttl    time.Duration

	mu    sync.Mutex
	cache map[string]osvCacheEntry
}

type osvCacheEntry struct {
	at      time.Time
	records []VulnRecord
}

// NewOSVProvider returns an OSVProvider with sane defaults: 5-second
// per-request timeout, 1-hour cache TTL, default httpClient. Inject a
// custom client in tests via the returned provider's exported fields.
func NewOSVProvider() *OSVProvider {
	return &OSVProvider{
		client: &http.Client{Timeout: 5 * time.Second},
		url:    "https://api.osv.dev/v1/query",
		ttl:    time.Hour,
		cache:  make(map[string]osvCacheEntry, 64),
	}
}

// SetClient swaps the underlying HTTP client. Use to inject a test
// server in unit tests.
func (p *OSVProvider) SetClient(c *http.Client, url string) {
	p.client = c
	if url != "" {
		p.url = url
	}
}

// Lookup queries osv.dev for a single dependency. The shape is:
//
//	POST /v1/query { "package": {"name": …, "ecosystem": …},
//	                 "version": … }
//
// Response is decoded into a minimal shape — only id, summary,
// database_specific.severity are read.
func (p *OSVProvider) Lookup(ctx context.Context, dep Dependency) ([]VulnRecord, error) {
	if dep.Name == "" || dep.Version == "" {
		return nil, nil
	}
	key := fmt.Sprintf("%s|%s|%s", dep.Ecosystem, dep.Name, dep.Version)
	if cached, ok := p.cached(key); ok {
		return cached, nil
	}

	body, err := json.Marshal(osvQueryBody{
		Package: osvPackage{Name: dep.Name, Ecosystem: string(dep.Ecosystem)},
		Version: dep.Version,
	})
	if err != nil {
		return nil, nil // conservative: never fail the scan from this path
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(body))
	if err != nil {
		return nil, nil
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, nil // network failure — fall back to offline
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil
	}
	var parsed osvQueryResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, nil
	}
	out := make([]VulnRecord, 0, len(parsed.Vulns))
	for _, v := range parsed.Vulns {
		out = append(out, VulnRecord{
			Ecosystem:   dep.Ecosystem,
			Name:        dep.Name,
			MaxAffected: dep.Version,
			ID:          v.ID,
			Summary:     v.Summary,
			Severity:    severityFromOSV(v.Severity, v.DatabaseSpecific),
		})
	}
	p.put(key, out)
	return out, nil
}

func (p *OSVProvider) cached(key string) ([]VulnRecord, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.cache[key]
	if !ok {
		return nil, false
	}
	if time.Since(e.at) > p.ttl {
		delete(p.cache, key)
		return nil, false
	}
	return e.records, true
}

func (p *OSVProvider) put(key string, recs []VulnRecord) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cache[key] = osvCacheEntry{at: time.Now(), records: recs}
}

// osvQueryBody / osvPackage / osvQueryResponse model the subset of the
// OSV.dev v1 query schema we use. Unrelated fields are deliberately
// omitted so unknown keys don't fail decoding.
type osvQueryBody struct {
	Package osvPackage `json:"package"`
	Version string     `json:"version"`
}

type osvPackage struct {
	Name      string `json:"name"`
	Ecosystem string `json:"ecosystem"`
}

type osvQueryResponse struct {
	Vulns []osvVuln `json:"vulns"`
}

type osvVuln struct {
	ID               string        `json:"id"`
	Summary          string        `json:"summary"`
	Severity         []osvSeverity `json:"severity"`
	DatabaseSpecific osvDBSpec     `json:"database_specific"`
}

type osvSeverity struct {
	Type  string `json:"type"`
	Score string `json:"score"`
}

type osvDBSpec struct {
	Severity string `json:"severity"`
}

func severityFromOSV(sevs []osvSeverity, dbs osvDBSpec) Severity {
	tag := dbs.Severity
	for _, s := range sevs {
		if s.Type == "CVSS_V3" || s.Type == "CVSS_V4" {
			if tag == "" {
				tag = s.Score
			}
		}
	}
	switch {
	case containsAny(tag, "CRITICAL", "critical"):
		return SeverityCritical
	case containsAny(tag, "HIGH", "high"):
		return SeverityError
	case containsAny(tag, "MEDIUM", "moderate", "MODERATE"):
		return SeverityWarning
	case containsAny(tag, "LOW", "low"):
		return SeverityInfo
	}
	return SeverityWarning
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if sub == "" {
			continue
		}
		if len(s) >= len(sub) {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}
