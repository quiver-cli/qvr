package security

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/quiver-cli/qvr/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingProvider is a minimal LLMProvider for exercising the
// scanner's semantic seam without an actual model.
type recordingProvider struct {
	mu       sync.Mutex
	calls    int
	response string
}

func (*recordingProvider) Name() string { return "recording" }

func (p *recordingProvider) Complete(_ context.Context, _ string) (string, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	return p.response, nil
}

func TestLLMCheckBuiltinStubsNoOp(t *testing.T) {
	prov := &recordingProvider{response: "no findings"}
	for _, c := range BuiltinLLMChecks() {
		findings := c.Run(context.Background(), prov, nil, nil)
		assert.Empty(t, findings, "%s stub must return no findings", c.Name())
	}
	assert.Equal(t, 0, prov.calls,
		"stubs must not call the provider (concrete impls are out of scope)")
}

func TestScannerSkipsLLMChecksWithoutProvider(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, dir, "SKILL.md", "# clean")
	s := NewWithChecks(stubCheck{name: "x"}).AddLLM(SemanticSecurityDiscoveryCheck{})
	res, err := s.Scan(context.Background(), nil, dir)
	require.NoError(t, err)
	assert.Empty(t, res.Findings)
}

func TestScannerRunsLLMChecksWithProvider(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, dir, "SKILL.md", "# x")
	prov := &recordingProvider{}

	called := false
	llm := llmStub{fn: func() []Finding {
		called = true
		return []Finding{{Check: "stub_llm", Severity: SeverityInfo, Message: "ok"}}
	}}

	s := NewWithChecks().WithLLMProvider(prov).AddLLM(llm)
	res, err := s.Scan(context.Background(), nil, dir)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.True(t, called, "LLM check should run when provider is configured")
	require.Len(t, res.Findings, 1)
	assert.Equal(t, "stub_llm", res.Findings[0].Check)
}

func TestLLMProviderFromEnv_NotConfigured(t *testing.T) {
	_ = os.Unsetenv(EnvLLMProvider)
	assert.Nil(t, LLMProviderFromEnv())
}

type llmStub struct{ fn func() []Finding }

func (llmStub) Name() string { return "stub" }
func (s llmStub) Run(_ context.Context, _ LLMProvider, _ *model.Skill, _ []FileEntry) []Finding {
	return s.fn()
}
