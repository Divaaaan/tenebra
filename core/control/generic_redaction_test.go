package control

import (
	"strings"
	"testing"
)

func TestGenericURLDiagnosticSanitizer(t *testing.T) {
	raw := "request failed https://user:PASS_CANARY@arbitrary.test/sub/PATH_CANARY?key=QUERY_CANARY#FRAGMENT_CANARY"
	got := scrubSecrets(raw)
	for _, secret := range []string{"PASS_CANARY", "PATH_CANARY", "QUERY_CANARY", "FRAGMENT_CANARY"} {
		if strings.Contains(got, secret) {
			t.Fatal("generic URL credential/path/query/fragment escaped diagnostic masking")
		}
	}
	if !strings.Contains(got, "arbitrary.test") {
		t.Fatal("diagnostic lost nonsecret provider context")
	}
}
