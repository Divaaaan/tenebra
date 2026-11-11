package installtx

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"io"
	"strings"
	"testing"

	"golang.org/x/crypto/blake2b"
)

func signatureFixture(t *testing.T, algorithm string, bodies ...[]byte) ([]byte, []byte, string) {
	t.Helper()
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{37}, ed25519.SeedSize))
	id := []byte("testkey1")
	body := []byte("synthetic installer container\n")
	if len(bodies) > 0 {
		body = bodies[0]
	}
	message := body
	if algorithm == "ED" {
		hash := blake2b.Sum512(body)
		message = hash[:]
	}
	signature := ed25519.Sign(key, message)
	comment := "timestamp:1 file:synthetic.tar"
	global := ed25519.Sign(key, append(append([]byte{}, signature...), []byte(comment)...))
	packet := append(append([]byte(algorithm), id...), signature...)
	text := "untrusted comment: synthetic test signature\n" + base64.StdEncoding.EncodeToString(packet) + "\ntrusted comment: " + comment + "\n" + base64.StdEncoding.EncodeToString(global) + "\n"
	public := append(append([]byte("Ed"), id...), key.Public().(ed25519.PublicKey)...)
	pub := "untrusted comment: synthetic public key\n" + base64.StdEncoding.EncodeToString(public) + "\n"
	return body, []byte(text), pub
}

func TestVerifyDetachedEdAndEDAuthenticateBothSignatures(t *testing.T) {
	for _, algorithm := range []string{"Ed", "ED"} {
		t.Run(algorithm, func(t *testing.T) {
			body, signature, public := signatureFixture(t, algorithm)
			if err := VerifyDetached(bytes.NewReader(body), signature, public); err != nil {
				t.Fatal(err)
			}
			if err := VerifyDetached(bytes.NewReader(body), []byte(base64.StdEncoding.EncodeToString(signature)), base64.StdEncoding.EncodeToString([]byte(public))); err != nil {
				t.Fatal(err)
			}
			if err := VerifyDetached(bytes.NewReader(body), []byte(base64.StdEncoding.EncodeToString(signature)+"\n"), base64.StdEncoding.EncodeToString([]byte(public))+"\n"); err != nil {
				t.Fatal(err)
			}
			if err := VerifyDetached(bytes.NewReader(append(append([]byte{}, body...), '!')), signature, public); !errors.Is(err, ErrSignature) {
				t.Fatalf("modified message accepted: %v", err)
			}
			lines := strings.Split(strings.TrimSuffix(string(signature), "\n"), "\n")
			mutations := map[string]func([]string){
				"comment": func(s []string) { s[2] += " forged" },
				"global":  func(s []string) { s[3] = base64.StdEncoding.EncodeToString(make([]byte, 64)) },
				"key-id": func(s []string) {
					raw, _ := base64.StdEncoding.DecodeString(s[1])
					raw[2] ^= 1
					s[1] = base64.StdEncoding.EncodeToString(raw)
				},
				"algorithm": func(s []string) {
					raw, _ := base64.StdEncoding.DecodeString(s[1])
					raw[0] = 'X'
					s[1] = base64.StdEncoding.EncodeToString(raw)
				},
				"extra-line":        func(s []string) { s[3] += "\nextra" },
				"base64-whitespace": func(s []string) { s[1] += " " },
				"nul-comment":       func(s []string) { s[2] += "\x00" },
			}
			for name, mutate := range mutations {
				t.Run(name, func(t *testing.T) {
					changed := append([]string{}, lines...)
					mutate(changed)
					if err := VerifyDetached(bytes.NewReader(body), []byte(strings.Join(changed, "\n")+"\n"), public); !errors.Is(err, ErrSignature) {
						t.Fatalf("modified signature accepted: %v", err)
					}
				})
			}
		})
	}
}

type signatureReadFailure struct{}

func (signatureReadFailure) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

type boundedSignatureReader struct {
	reader  io.Reader
	largest int
}

func (r *boundedSignatureReader) Read(p []byte) (int, error) {
	r.largest = max(r.largest, len(p))
	if len(p) > 32<<10 {
		return 0, errors.New("signature verifier allocated a growing message buffer")
	}
	return r.reader.Read(p)
}
func TestVerifyDetachedStreamsLegacyWithoutGrowingReads(t *testing.T) {
	for _, algorithm := range []string{"Ed", "ED"} {
		body, signature, pub := signatureFixture(t, algorithm, bytes.Repeat([]byte("synthetic-stream-only"), 32768))
		reader := &boundedSignatureReader{reader: bytes.NewReader(body)}
		if err := VerifyDetached(reader, signature, pub); err != nil {
			t.Fatalf("%s stream: %v (largest read %d)", algorithm, err, reader.largest)
		}
	}
}

func TestVerifyDetachedReadFailureAndBadFraming(t *testing.T) {
	body, signature, public := signatureFixture(t, "ED")
	if err := VerifyDetached(signatureReadFailure{}, signature, public); err == nil {
		t.Fatal("reader failure accepted")
	}
	for _, input := range [][]byte{nil, []byte("!!!!"), bytes.Repeat([]byte{'a'}, 16385), append(signature, '\n')} {
		if err := VerifyDetached(bytes.NewReader(body), input, public); !errors.Is(err, ErrSignature) {
			t.Fatalf("bad framing accepted: %v", err)
		}
	}
	if err := VerifyDetached(bytes.NewReader(body), signature, ""); !errors.Is(err, ErrSignature) {
		t.Fatal("missing public key accepted")
	}
}
