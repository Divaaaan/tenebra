package subscription

import (
	"strings"
	"testing"
)

func TestParseLinksMixedValidInvalidBlank(t *testing.T) {
	// One multi-line block holding good links, blanks, comments and junk. The
	// good ones must parse in order; the two link-shaped junk lines count as
	// skipped; blanks and comments count as neither.
	block := strings.Join([]string{
		"# my servers",
		"vless://abc@example.com:443?security=tls#a",
		"",
		"// inline note",
		"this-is-not-a-link", // no scheme -> skipped
		"vmess://%%%bad",     // bad base64 -> skipped
		"trojan://pw@example.com:443#t",
		"   ", // whitespace-only -> ignored
	}, "\n")

	nodes, skipped := ParseLinks([]string{block})
	if len(nodes) != 2 {
		t.Fatalf("len(nodes) = %d, want 2", len(nodes))
	}
	if skipped != 2 {
		t.Errorf("skipped = %d, want 2", skipped)
	}
	if nodes[0].Name != "a" || nodes[1].Name != "t" {
		t.Errorf("node names = %q, %q; want a, t", nodes[0].Name, nodes[1].Name)
	}
}

func TestParseLinksDeduplicates(t *testing.T) {
	// The same link three times (across slice entries and within a block)
	// collapses to a single node; order follows first occurrence.
	a := "vless://abc@example.com:443?security=tls#a"
	b := "trojan://pw@example.com:443#b"
	nodes, skipped := ParseLinks([]string{
		a,
		a + "\n" + b,
		a,
	})
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}
	if len(nodes) != 2 {
		t.Fatalf("len(nodes) = %d, want 2 (deduped)", len(nodes))
	}
	if nodes[0].Name != "a" || nodes[1].Name != "b" {
		t.Errorf("node names = %q, %q; want a, b", nodes[0].Name, nodes[1].Name)
	}
}

func TestParseLinksAllInvalid(t *testing.T) {
	nodes, skipped := ParseLinks([]string{"nope", "also-bad", "vmess://%%%"})
	if len(nodes) != 0 {
		t.Errorf("len(nodes) = %d, want 0", len(nodes))
	}
	if skipped != 3 {
		t.Errorf("skipped = %d, want 3", skipped)
	}
}

func TestParseLinksEmptyAndCommentsOnly(t *testing.T) {
	// Nothing link-shaped at all: no nodes and, crucially, nothing skipped —
	// blanks and comments are not failures.
	nodes, skipped := ParseLinks([]string{"", "  \n # comment \n // note", "\n\n"})
	if len(nodes) != 0 {
		t.Errorf("len(nodes) = %d, want 0", len(nodes))
	}
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}
}

func TestParseLinksTrimsSurroundingWhitespace(t *testing.T) {
	// Leading/trailing whitespace (incl. a stray \r from CRLF input) must not
	// stop a link from parsing.
	nodes, skipped := ParseLinks([]string{"  \tvless://abc@example.com:443?security=tls#a\r"})
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}
	if len(nodes) != 1 || nodes[0].Name != "a" {
		t.Fatalf("got %d nodes, want 1 named 'a'", len(nodes))
	}
}
