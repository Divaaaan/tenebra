package routing

import "regexp"

// matchesProcess evaluates the generated matcher against an executable name.
// It never treats a regexp's textual representation as a process identity.
func matchesProcess(patterns []string, name string) bool {
	for _, pattern := range patterns {
		matched, err := regexp.MatchString(pattern, name)
		if err == nil && matched {
			return true
		}
	}
	return false
}
