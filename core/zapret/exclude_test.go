package zapret

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readExclude(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "lists", excludeFile))
	if err != nil {
		t.Fatalf("read exclude list: %v", err)
	}
	return string(data)
}

// The exclusion is what stops the bypass from desyncing the tunnel's own
// handshake: the exit node is served on one of the very ports winws watches, and
// a mangled ClientHello there looks exactly like a dead node.
func TestExcludeNodesListsNodeAddresses(t *testing.T) {
	dir := t.TempDir()

	if err := ExcludeNodes(dir, []string{"95.163.176.178", "[2001:db8::1]", "node.example.com", ""}); err != nil {
		t.Fatalf("ExcludeNodes: %v", err)
	}

	got := readExclude(t, dir)
	if !strings.Contains(got, "95.163.176.178") {
		t.Error("the node address was not excluded")
	}
	if !strings.Contains(got, "2001:db8::1") {
		t.Error("a bracketed IPv6 node address was not excluded")
	}
	// A hostname is skipped rather than resolved: a lookup here can run before the
	// network is usable, and a stale answer would exclude a stranger's address
	// while leaving the node itself exposed.
	if strings.Contains(got, "node.example.com") {
		t.Error("a hostname was written into an IP-only list")
	}
}

// The file is user-editable and the bundle ships it for exactly that purpose, so
// rewriting it must not eat what the user put there.
func TestExcludeNodesKeepsUserEntries(t *testing.T) {
	dir := t.TempDir()
	lists := filepath.Join(dir, "lists")
	if err := os.MkdirAll(lists, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lists, excludeFile), []byte("# mine\r\n203.0.113.7\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := ExcludeNodes(dir, []string{"95.163.176.178"}); err != nil {
		t.Fatalf("ExcludeNodes: %v", err)
	}
	if err := ExcludeNodes(dir, []string{"107.189.22.68"}); err != nil {
		t.Fatalf("ExcludeNodes rewrite: %v", err)
	}

	got := readExclude(t, dir)
	if !strings.Contains(got, "203.0.113.7") || !strings.Contains(got, "# mine") {
		t.Errorf("user entries were lost:\n%s", got)
	}
	if !strings.Contains(got, "107.189.22.68") {
		t.Error("the current node list was not written")
	}
	// The managed block is replaced, not appended to, so a node dropped from the
	// subscription stops being excluded.
	if strings.Contains(got, "95.163.176.178") {
		t.Error("a stale node address survived the rewrite")
	}
	if strings.Count(got, excludeHeader) != 1 {
		t.Errorf("the managed header was duplicated:\n%s", got)
	}
}
