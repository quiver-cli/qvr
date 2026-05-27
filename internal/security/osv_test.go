package security

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOfflineProviderLookup(t *testing.T) {
	dep := Dependency{
		Ecosystem: EcosystemPyPI,
		Name:      "pyyaml",
		Version:   "5.3.1",
	}
	recs, err := OfflineProvider{}.Lookup(context.Background(), dep)
	require.NoError(t, err)
	require.NotEmpty(t, recs)
	assert.Equal(t, "CVE-2020-14343", recs[0].ID)
}

func TestOfflineProviderClean(t *testing.T) {
	dep := Dependency{
		Ecosystem: EcosystemPyPI,
		Name:      "requests",
		Version:   "2.32.0",
	}
	recs, err := OfflineProvider{}.Lookup(context.Background(), dep)
	require.NoError(t, err)
	assert.Empty(t, recs)
}

func TestOSVProvider_OnlineHappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(osvQueryResponse{
			Vulns: []osvVuln{{
				ID:               "GHSA-aaaa-bbbb-cccc",
				Summary:          "test vuln",
				DatabaseSpecific: osvDBSpec{Severity: "HIGH"},
			}},
		})
	}))
	defer srv.Close()

	p := NewOSVProvider()
	p.SetClient(srv.Client(), srv.URL)
	recs, err := p.Lookup(context.Background(), Dependency{
		Ecosystem: EcosystemNPM, Name: "x", Version: "1.0.0",
	})
	require.NoError(t, err)
	require.Len(t, recs, 1)
	assert.Equal(t, "GHSA-aaaa-bbbb-cccc", recs[0].ID)
	assert.Equal(t, SeverityError, recs[0].Severity)
}

func TestOSVProvider_NetworkFailureFallsBackQuietly(t *testing.T) {
	p := NewOSVProvider()
	// Point at a definitely-closed port so the request errors.
	p.SetClient(&http.Client{Transport: failingRoundTripper{}}, "http://127.0.0.1:1/query")
	recs, err := p.Lookup(context.Background(), Dependency{
		Ecosystem: EcosystemPyPI, Name: "x", Version: "1.0.0",
	})
	// Spec: returns nil error, nil records on transport failure so the
	// caller can fall back to the offline DB without disrupting scans.
	assert.NoError(t, err)
	assert.Empty(t, recs)
}

func TestOSVProvider_Cache(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(osvQueryResponse{})
	}))
	defer srv.Close()

	p := NewOSVProvider()
	p.SetClient(srv.Client(), srv.URL)
	dep := Dependency{Ecosystem: EcosystemPyPI, Name: "cached", Version: "1.0.0"}
	for i := 0; i < 3; i++ {
		_, _ = p.Lookup(context.Background(), dep)
	}
	assert.Equal(t, 1, calls, "subsequent lookups must hit the cache")
}

func TestSeverityFromOSV(t *testing.T) {
	cases := []struct {
		dbs  string
		want Severity
	}{
		{"CRITICAL", SeverityCritical},
		{"HIGH", SeverityError},
		{"MEDIUM", SeverityWarning},
		{"LOW", SeverityInfo},
		{"", SeverityWarning},
	}
	for _, c := range cases {
		t.Run(c.dbs, func(t *testing.T) {
			assert.Equal(t, c.want, severityFromOSV(nil, osvDBSpec{Severity: c.dbs}))
		})
	}
}

type failingRoundTripper struct{}

func (failingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, http.ErrHandlerTimeout
}
