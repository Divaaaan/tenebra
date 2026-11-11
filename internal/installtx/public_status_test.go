package installtx

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestPublicStatusUsesExactSanitizedP0DTO(t *testing.T) {
	status := journalStatus(installerDisk().journal)
	body, err := EncodePublicStatus(status)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil || len(fields) != 8 {
		t.Fatal("public fields drifted", fields, err)
	}
	for _, key := range []string{"path", "core_sha256", "runtime", "profiles", "source", "url", "pid", "start_token", "launch_id"} {
		if _, ok := fields[key]; ok {
			t.Fatal("private data reached public status", key)
		}
	}
	got, err := DecodePublicStatus(body)
	if err != nil || !reflect.DeepEqual(got, status) {
		t.Fatal(got, err)
	}
	if !bytes.HasSuffix(body, []byte("\n")) {
		t.Fatal("status is not a complete JSON frame")
	}
}

func TestPublicStatusRejectsPathsIDsAndFalseAdmission(t *testing.T) {
	valid := journalStatus(installerDisk().journal)
	for _, edit := range []func(*Status){
		func(s *Status) { s.ActiveVersion = "/private/profile.json" },
		func(s *Status) { s.PendingVersion = "https://secret.invalid/profile" },
		func(s *Status) { s.ErrorCode = "error at /private/path" },
		func(s *Status) { s.TransactionID = "it1_" + strings.Repeat("A", 32) },
		func(s *Status) { s.OperationalCommitID = "wrong" },
		func(s *Status) { s.Phase = "fencing"; s.BusinessAdmission = "open" },
		func(s *Status) { s.BusinessAdmission = "unchanged" },
		func(s *Status) { s.Phase = "inactive" },
	} {
		status := valid
		edit(&status)
		if _, err := EncodePublicStatus(status); err == nil {
			t.Fatal("unsafe status was publishable", status)
		}
	}
}

func TestPublicStatusRequiresEveryFieldAsAString(t *testing.T) {
	body, err := EncodePublicStatus(journalStatus(installerDisk().journal))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "pending_version")
	bad, _ := json.Marshal(fields)
	if _, err := DecodePublicStatus(bad); err == nil {
		t.Fatal("missing public field accepted")
	}
	fields["pending_version"] = nil
	bad, _ = json.Marshal(fields)
	if _, err := DecodePublicStatus(bad); err == nil {
		t.Fatal("null public field accepted")
	}
	fields["pending_version"] = ""
	fields["profile_url"] = "synthetic-secret"
	bad, _ = json.Marshal(fields)
	if _, err := DecodePublicStatus(bad); err == nil {
		t.Fatal("unexpected private field accepted")
	}
	bad = bytes.Replace(body, []byte(`"phase":`), []byte(`"phase":"committed_open","phase":`), 1)
	if _, err := DecodePublicStatus(bad); err == nil {
		t.Fatal("duplicate public field accepted")
	}
}
