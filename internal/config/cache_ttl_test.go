// Package config_test exercises internal/config through its public API only.
package config_test

import (
	"testing"
	"time"

	"github.com/astra-sh/qvr/internal/config"
)

func TestParseCacheTTL(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    time.Duration
		wantErr bool
	}{
		{name: "empty falls back to default 1h", raw: "", want: config.DefaultCacheTTL},
		{name: "whitespace falls back to default", raw: "   ", want: config.DefaultCacheTTL},
		{name: "zero means always rebuild", raw: "0", want: 0},
		{name: "15m", raw: "15m", want: 15 * time.Minute},
		{name: "2h", raw: "2h", want: 2 * time.Hour},
		{name: "30s", raw: "30s", want: 30 * time.Second},
		{name: "compound 1h30m", raw: "1h30m", want: 90 * time.Minute},
		{name: "negative rejected", raw: "-1h", wantErr: true},
		{name: "garbage rejected", raw: "potato", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := config.ParseCacheTTL(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
