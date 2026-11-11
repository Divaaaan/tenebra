package routing

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ValidAppName accepts a literal executable basename, never a path or pattern.
func ValidAppName(name string) bool {
	return name != "" && name != "." && name != ".." &&
		utf8.ValidString(name) && strings.TrimSpace(name) == name &&
		!strings.ContainsAny(name, "/\\\x00\r\n")
}

// foldAppKey follows the same Unicode simple-fold equivalence as RE2's (?i).
func foldAppKey(name string) string {
	return strings.Map(func(r rune) rune {
		key := r
		for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
			if next < key {
				key = next
			}
		}
		return key
	}, name)
}

func processPathPatterns(names []string) []string {
	if len(names) == 0 {
		return nil
	}
	patterns := make([]string, 0, len(names))
	for _, name := range names {
		patterns = append(patterns, `(?i)(?:^|[\\/])`+regexp.QuoteMeta(name)+`$`)
	}
	return patterns
}

// ReservedTags returns the builder-owned routing names without sharing storage.
func ReservedTags() []string {
	return []string{tagProxy, tagDirect, dnsRemoteTag, dnsDirectTag,
		ruleSetGeoIPRU, ruleSetGeositeRU, ruleSetGeositeAds}
}
