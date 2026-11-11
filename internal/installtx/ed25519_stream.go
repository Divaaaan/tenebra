package installtx

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha512"
	"io"

	"filippo.io/edwards25519"
)

// Raw minisign Ed uses standard Ed25519, not Ed25519ph. Its verification
// equation consumes SHA512(R || A || message), so a streamed hash is sufficient.
// Group operations use the same edwards25519 implementation extracted from Go;
// no private key operations or custom field arithmetic are implemented here.
func verifyEd25519Stream(pub ed25519.PublicKey, signature []byte, message io.Reader, maxBytes int64) error {
	if len(pub) != ed25519.PublicKeySize || len(signature) != ed25519.SignatureSize || maxBytes <= 0 || maxBytes > MaxContainerBytes || signature[63]&224 != 0 {
		return ErrSignature
	}
	point, err := new(edwards25519.Point).SetBytes(pub)
	if err != nil {
		return ErrSignature
	}
	scalar, err := new(edwards25519.Scalar).SetCanonicalBytes(signature[32:])
	if err != nil {
		return ErrSignature
	}
	hash := sha512.New()
	hash.Write(signature[:32])
	hash.Write(pub)
	n, err := io.Copy(hash, io.LimitReader(message, maxBytes+1))
	if err != nil {
		return err
	}
	if n > maxBytes {
		return ErrSignature
	}
	challenge, err := new(edwards25519.Scalar).SetUniformBytes(hash.Sum(nil))
	if err != nil {
		return ErrSignature
	}
	minusA := new(edwards25519.Point).Negate(point)
	result := new(edwards25519.Point).VarTimeDoubleScalarBaseMult(challenge, minusA, scalar)
	if !bytes.Equal(result.Bytes(), signature[:32]) {
		return ErrSignature
	}
	return nil
}
