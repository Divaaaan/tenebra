package nodeidentity

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestIdentityNewID128BitsAndFailure(t *testing.T) {
	id, err := newID(bytes.NewReader(bytes.Repeat([]byte{0x11}, 16)))
	if err != nil || id != "n2_11111111111111111111111111111111" {
		t.Fatalf("id=%q error=%v", id, err)
	}
	if id, err := newID(bytes.NewReader(make([]byte, 15))); id != "" || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("partial RNG accepted: %q %v", id, err)
	}
	for _, id := range []string{"", "n2_" + strings.Repeat("a", 31), "n2_" + strings.Repeat("a", 33), "n2_" + strings.Repeat("A", 32), "n2_" + strings.Repeat("z", 32), "xx_" + strings.Repeat("a", 32)} {
		if ValidID(id) {
			t.Fatalf("invalid opaque ID accepted: %q", id)
		}
	}
	if !ValidID("n2_11111111111111111111111111111111") {
		t.Fatal("valid ID refused")
	}
}

func TestIdentityRedactionLeavesOpaqueIDAndTypedProfileAlone(t *testing.T) {
	opaque := "n2_11111111111111111111111111111111"
	if got := RedactLegacyIDs("node=0123456789abcdef, next=" + opaque); got != "node=[legacy-node-id], next="+opaque {
		t.Fatalf("redaction=%q", got)
	}
}
