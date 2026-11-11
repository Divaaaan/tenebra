package installtx

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"io"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/blake2b"
)

const signatureTextLimit = 16384

func canonicalBase64(text string) ([]byte, error) {
	if len(text) == 0 || len(text) > signatureTextLimit || strings.ContainsAny(text, "\r\n\t ") {
		return nil, ErrSignature
	}
	data, err := base64.StdEncoding.Strict().DecodeString(text)
	if err != nil || base64.StdEncoding.EncodeToString(data) != text {
		return nil, ErrSignature
	}
	return data, nil
}
func minisignText(data []byte) ([]byte, error) {
	if len(data) == 0 || len(data) > signatureTextLimit {
		return nil, ErrSignature
	}
	if !bytes.HasPrefix(data, []byte("untrusted comment: ")) {
		envelope := strings.TrimSuffix(string(data), "\n")
		envelope = strings.TrimSuffix(envelope, "\r")
		decoded, err := canonicalBase64(envelope)
		if err != nil {
			return nil, ErrSignature
		}
		data = decoded
	}
	if !utf8.Valid(data) {
		return nil, ErrSignature
	}
	for _, b := range data {
		if b < 32 && b != '\n' && b != '\r' && b != '\t' || b == 127 {
			return nil, ErrSignature
		}
	}
	return data, nil
}
func minisignLines(data []byte, count int) ([]string, error) {
	data, err := minisignText(data)
	if err != nil {
		return nil, err
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if strings.ContainsRune(text, '\r') {
		return nil, ErrSignature
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) != count || !strings.HasPrefix(lines[0], "untrusted comment: ") {
		return nil, ErrSignature
	}
	return lines, nil
}
func minisignPublicKey(text string) ([]byte, error) {
	// Both bare -P keys and native/Tauri public-key envelopes are supported.
	if !strings.ContainsAny(text, "\r\n ") {
		raw, err := canonicalBase64(text)
		if err == nil && len(raw) == 42 && string(raw[:2]) == "Ed" {
			return raw, nil
		}
	}
	lines, err := minisignLines([]byte(text), 2)
	if err != nil {
		return nil, err
	}
	raw, err := canonicalBase64(lines[1])
	if err != nil || len(raw) != 42 || string(raw[:2]) != "Ed" {
		return nil, ErrSignature
	}
	return raw, nil
}

// VerifyDetached implements upstream minisign Ed (raw Ed25519) and ED
// (Ed25519 over streaming BLAKE2b-512), including the independent signature of
// signature || trusted-comment. Key IDs and all packet framing are verified.
// The public key is caller-owned policy, never read from an input manifest.
func VerifyDetached(reader io.Reader, signature []byte, publicKey string) error {
	if reader == nil {
		return ErrSignature
	}
	key, err := minisignPublicKey(publicKey)
	if err != nil {
		return ErrSignature
	}
	lines, err := minisignLines(signature, 4)
	if err != nil {
		return ErrSignature
	}
	packet, err := canonicalBase64(lines[1])
	if err != nil || len(packet) != 74 || !oneOf(string(packet[:2]), "Ed", "ED") || !bytes.Equal(packet[2:10], key[2:10]) ||
		!strings.HasPrefix(lines[2], "trusted comment: ") {
		return ErrSignature
	}
	global, err := canonicalBase64(lines[3])
	if err != nil || len(global) != ed25519.SignatureSize {
		return ErrSignature
	}
	pub := ed25519.PublicKey(key[10:])
	commentMessage := append(append([]byte{}, packet[10:]...), []byte(strings.TrimPrefix(lines[2], "trusted comment: "))...)
	if !ed25519.Verify(pub, commentMessage, global) {
		return ErrSignature
	}
	var message []byte
	limited := io.LimitReader(reader, MaxContainerBytes+1)
	if string(packet[:2]) == "ED" {
		hash, _ := blake2b.New512(nil)
		n, err := io.Copy(hash, limited)
		if err != nil {
			return err
		}
		if n > MaxContainerBytes {
			return ErrSignature
		}
		message = hash.Sum(nil)
	} else {
		return verifyEd25519Stream(pub, packet[10:], reader, MaxContainerBytes)
	}
	if !ed25519.Verify(pub, message, packet[10:]) {
		return ErrSignature
	}
	return nil
}
