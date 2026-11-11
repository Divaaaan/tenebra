package installtx

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"
)

func TestVerifyDetachedIndependentNodeVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/minisign-node-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct {
		Algorithm, Body, Signature string
		PublicKey                  string `json:"public_key"`
	}
	if err := json.Unmarshal(data, &vectors); err != nil || len(vectors) != 2 {
		t.Fatal("missing independent vectors", err)
	}
	for _, vector := range vectors {
		body, err := base64.StdEncoding.DecodeString(vector.Body)
		if err != nil {
			t.Fatal(err)
		}
		if err := VerifyDetached(bytes.NewReader(body), []byte(vector.Signature), vector.PublicKey); err != nil {
			t.Fatalf("%s Node vector: %v", vector.Algorithm, err)
		}
	}
}
func TestEd25519StreamMatchesStandardAndRejectsOverBudget(t *testing.T) {
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{83}, 32))
	pub := key.Public().(ed25519.PublicKey)
	for _, size := range []int{0, 1, 64, 512, 65537} {
		message := bytes.Repeat([]byte{99}, size)
		signature := ed25519.Sign(key, message)
		if err := verifyEd25519Stream(pub, signature, bytes.NewReader(message), 1<<20); err != nil {
			t.Fatal(size, err)
		}
		for offset := range 64 {
			changed := append([]byte{}, signature...)
			changed[offset] ^= 1
			got := verifyEd25519Stream(pub, changed, bytes.NewReader(message), 1<<20) == nil
			if got != ed25519.Verify(pub, message, changed) {
				t.Fatalf("standard verifier differs: size=%d offset=%d", size, offset)
			}
		}
	}
	message := bytes.Repeat([]byte{99}, 1025)
	signature := ed25519.Sign(key, message)
	if err := verifyEd25519Stream(pub, signature, bytes.NewReader(message), 1024); err == nil {
		t.Fatal("valid but over-budget message accepted")
	}
}
