package cli

import (
	"bytes"
	"testing"
)

func TestTransferSpinnerNoOpOnNonTTY(t *testing.T) {
	var buf bytes.Buffer
	stop := startTransferSpinner(&buf)
	stop()
	if buf.Len() != 0 {
		t.Fatalf("spinner must be silent on a non-TTY writer, got %q", buf.String())
	}
}
