package cmd

import (
	"testing"

	"github.com/raks097/quiver/internal/model"
)

func TestSignedColRendersNone(t *testing.T) {
	if got := signedCol(model.SignatureStatusNone); got != "none" {
		t.Fatalf("signedCol(none) = %q, want none", got)
	}
}
