package control

import (
	"encoding/hex"
	"strings"
)

// A Profile.ID is public typed identity. The same shape in free text may be a
// credential-derived old node hash and is always redacted there.
func legacyProfileID(value string) bool {
	decoded, err := hex.DecodeString(value)
	return len(value) == 16 && err == nil && len(decoded) == 8
}
func scrubDiagnosticText(text string, profileSpans [][2]int) string {
	var result strings.Builder
	previous := 0
	for _, span := range profileSpans {
		if span[0] < previous || span[1] < span[0] || span[1] > len(text) {
			return scrubSecrets(text)
		}
		result.WriteString(scrubSecrets(text[previous:span[0]]))
		result.WriteString(text[span[0]:span[1]])
		previous = span[1]
	}
	result.WriteString(scrubSecrets(text[previous:]))
	return result.String()
}
