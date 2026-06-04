package registry_test

import (
	"errors"
	"testing"

	"github.com/raks097/quiver/internal/config"
	"github.com/raks097/quiver/internal/registry"
)

func cfgWith(names ...string) *config.Config {
	cfg := &config.Config{Registries: map[string]config.RegistryConfig{}}
	for _, n := range names {
		cfg.Registries[n] = config.RegistryConfig{URL: "file:///" + n}
	}
	return cfg
}

func TestResolveName(t *testing.T) {
	tests := []struct {
		name       string
		configured []string
		input      string
		want       string
		wantErrIs  error  // sentinel to errors.Is against, nil if none
		wantErrSub string // substring the error must contain (for ambiguity)
	}{
		{
			name:       "empty resolves to empty (all registries)",
			configured: []string{"acme/skills"},
			input:      "",
			want:       "",
		},
		{
			name:       "exact full name passes through",
			configured: []string{"acme/skills", "example/tools"},
			input:      "acme/skills",
			want:       "acme/skills",
		},
		{
			name:       "unique leaf resolves to full name",
			configured: []string{"acme/skills", "example/tools"},
			input:      "skills",
			want:       "acme/skills",
		},
		{
			name:       "flat name still works as exact match",
			configured: []string{"internal-tools"},
			input:      "internal-tools",
			want:       "internal-tools",
		},
		{
			name:       "unknown name is not found",
			configured: []string{"acme/skills"},
			input:      "nope",
			wantErrIs:  registry.ErrRegistryNotFound,
		},
		{
			name:       "ambiguous leaf errors and names candidates",
			configured: []string{"acme/skills", "other/skills"},
			input:      "skills",
			wantErrSub: "ambiguous",
		},
		{
			name:       "exact match wins over leaf ambiguity",
			configured: []string{"skills", "other/skills"},
			input:      "skills",
			want:       "skills",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := registry.ResolveName(cfgWith(tt.configured...), tt.input)
			switch {
			case tt.wantErrIs != nil:
				if !errors.Is(err, tt.wantErrIs) {
					t.Fatalf("err = %v, want errors.Is %v", err, tt.wantErrIs)
				}
			case tt.wantErrSub != "":
				if err == nil || !contains(err.Error(), tt.wantErrSub) {
					t.Fatalf("err = %v, want substring %q", err, tt.wantErrSub)
				}
			default:
				if err != nil {
					t.Fatalf("unexpected err: %v", err)
				}
				if got != tt.want {
					t.Errorf("got %q, want %q", got, tt.want)
				}
			}
		})
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
