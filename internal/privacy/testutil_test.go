package privacy

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

// mockEvent is the privacy.Event implementation used throughout the
// test suite. It keeps paths and fields in plain maps so tests can
// assert post-mutation state directly.
type mockEvent struct {
	paths        []string
	stringFields map[string]string

	stripCalled   int
	redactCalled  int
	stripCleared  []string // field names zeroed on StripContent (for assertions)
	contentFields map[string]string
}

func newMockEvent() *mockEvent {
	return &mockEvent{
		stringFields:  map[string]string{},
		contentFields: map[string]string{},
	}
}

func (m *mockEvent) withPath(p string) *mockEvent {
	m.paths = append(m.paths, p)
	return m
}

func (m *mockEvent) withField(name, val string) *mockEvent {
	m.stringFields[name] = val
	return m
}

// withContentField marks a field that StripContent should zero. Used
// to verify content-stripping semantics without pulling in the ops
// package.
func (m *mockEvent) withContentField(name, val string) *mockEvent {
	m.stringFields[name] = val
	m.contentFields[name] = val
	return m
}

func (m *mockEvent) GetPaths() []string { return m.paths }
func (m *mockEvent) GetStringFields() map[string]string {
	out := make(map[string]string, len(m.stringFields))
	for k, v := range m.stringFields {
		out[k] = v
	}
	return out
}
func (m *mockEvent) StripContent() {
	m.stripCalled++
	for name := range m.contentFields {
		m.stringFields[name] = ""
		m.stripCleared = append(m.stripCleared, name)
	}
}
func (m *mockEvent) ApplyRedactions(r map[string]string) {
	m.redactCalled++
	for name, val := range r {
		m.stringFields[name] = val
	}
}

// readFixtureLines returns non-blank, non-comment lines from
// testdata/<name>. Accepts testing.TB so unit tests and fuzz seeders
// can share this helper.
func readFixtureLines(tb testing.TB, name string) []string {
	tb.Helper()
	f, err := os.Open("testdata/" + name)
	if err != nil {
		tb.Fatalf("open fixture %q: %v", name, err)
	}
	defer f.Close()
	var out []string
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	if err := s.Err(); err != nil {
		tb.Fatalf("scan fixture %q: %v", name, err)
	}
	return out
}

// readFixturePairs returns parsed <label>|<value> lines from
// testdata/<name>.
func readFixturePairs(tb testing.TB, name string) []labeledPair {
	tb.Helper()
	var out []labeledPair
	for _, line := range readFixtureLines(tb, name) {
		idx := strings.IndexByte(line, '|')
		if idx < 0 {
			tb.Fatalf("fixture %q: malformed line %q (missing |)", name, line)
		}
		out = append(out, labeledPair{
			Label: line[:idx],
			Value: line[idx+1:],
		})
	}
	return out
}

type labeledPair struct {
	Label string
	Value string
}

func contains(xs []string, target string) bool {
	for _, x := range xs {
		if x == target {
			return true
		}
	}
	return false
}
