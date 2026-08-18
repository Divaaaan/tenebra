package zapret

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// hostlistFiles are the bundle's domain lists — the exact set every shipped
// strategy passes to winws as --hostlist.
//
// They are what decides which destinations the bypass touches at all: winws
// leaves anything outside them alone. That makes the lists the only honest
// answer to "what does the bypass cover", which is the question routing has to
// answer before it can hand a service to the direct path instead of the tunnel.
var hostlistFiles = []string{
	"list-general.txt",
	"list-general-user.txt",
	"list-google.txt",
}

// placeholderDomains are entries the bundle ships to keep a list non-empty
// rather than to match anything. list-general-user.txt carries one with the
// comment "Never leave this file empty"; treating it as covered would be
// harmless in itself, but it makes the coverage report lie.
var placeholderDomains = map[string]bool{
	"domain.example.abc": true,
}

// Covered reads the installed bundle's host lists and returns the domains the
// bypass acts on, lowercased, de-duplicated and sorted.
//
// A missing or unreadable list is not an error: the lists are optional per
// strategy, and an empty result simply means "coverage unknown", which the
// caller must treat as "cover nothing extra" rather than "cover everything".
// The failure mode this guards against is concrete — assuming the bypass covers
// a service it does not (say api.anthropic.com, which no shipped list mentions)
// routes that service direct into a censor that answers with a forged 403.
func Covered(dir string) []string {
	seen := make(map[string]struct{})
	for _, name := range hostlistFiles {
		readHostlist(filepath.Join(dir, "lists", name), seen)
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for d := range seen {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// readHostlist adds one list file's domains to seen. Comment lines (#), blank
// lines and the bundle's placeholder entries are skipped; a leading "*." or "."
// is stripped so the entry is a plain suffix.
func readHostlist(path string, seen map[string]struct{}) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.ToLower(line)
		line = strings.TrimPrefix(line, "*.")
		line = strings.TrimPrefix(line, ".")
		line = strings.TrimSuffix(line, ".")
		if line == "" || placeholderDomains[line] {
			continue
		}
		seen[line] = struct{}{}
	}
}
