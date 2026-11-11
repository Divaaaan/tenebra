package securefs

import (
	"context"
	"runtime"
	"sort"
	"strings"
)

// CopyFilesToStage copies only named regular leaf objects in an operator input
// directory, using CopyToStage's same pinned-object stream/ownership/swap checks.
// It neither enumerates nor reads unrelated files beside a selected container.
func CopyFilesToStage(ctx context.Context, source *Root, stage *Stage, files []string, limits Limits) error {
	if len(files) == 0 || !validLimits(limits) || len(files) > limits.MaxFiles {
		return ErrLimit
	}
	names := append([]string{}, files...)
	seen := map[string]bool{}
	for _, name := range names {
		parts, err := relativeParts(name)
		if err != nil || len(parts) != 1 || name == sealName {
			return ErrUntrusted
		}
		key := name
		if runtime.GOOS == "windows" {
			key = strings.ToLower(name)
		}
		if seen[key] {
			return ErrUntrusted
		}
		seen[key] = true
	}
	sort.Strings(names)
	return copySelectedToStage(ctx, source, stage, limits, names)
}
