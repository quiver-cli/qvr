package cmd

import (
	"bytes"
	"testing"

	"github.com/quiver-cli/qvr/internal/output"
)

// withCapturingPrinter swaps the package-level printer for a buffer-backed
// one so tests can assert on rendered output. Restores the previous printer
// on cleanup. Originally lived in push_test.go (removed when `qvr push` was
// folded into `qvr publish`); kept here so the shared lock_test / sync_test
// callers don't have to re-implement the same swap.
func withCapturingPrinter(t *testing.T, format output.Format) *bytes.Buffer {
	t.Helper()
	stdout := &bytes.Buffer{}
	prev := printer
	printer = &output.Printer{Out: stdout, Err: &bytes.Buffer{}, Format: format}
	t.Cleanup(func() { printer = prev })
	return stdout
}
