// Package nodeidentity provides opaque public IDs and private spec matching.
package nodeidentity

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"regexp"
)

func NewID() (string, error) { return newID(rand.Reader) }

func newID(random io.Reader) (string, error) {
	var bytes [16]byte
	if _, err := io.ReadFull(random, bytes[:]); err != nil {
		return "", err
	}
	return "n2_" + hex.EncodeToString(bytes[:]), nil
}

func ValidID(id string) bool {
	if len(id) != 35 || id[:3] != "n2_" {
		return false
	}
	for _, char := range id[3:] {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

var legacyToken = regexp.MustCompile(`\b[0-9a-fA-F]{16}\b`)

// RedactLegacyIDs is for free-form diagnostics. Typed Profile.ID is retained.
func RedactLegacyIDs(message string) string {
	return legacyToken.ReplaceAllString(message, "[legacy-node-id]")
}
