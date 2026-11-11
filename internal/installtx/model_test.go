package installtx

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func testRuntime() RuntimeRef {
	return RuntimeRef{ArtifactSHA256: strings.Repeat("a", 64), Version: "0.6.0", HelperSHA256: strings.Repeat("b", 64), Provenance: "release"}
}

func TestRequestStrictTaggedUnion(t *testing.T) {
	good := []string{
		`{"schema":1,"operation_id":"so1_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","action":"install","restart_ack":"restart","trust":"release","expected_version":"0.6.0","expected_source_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","expected_payload_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","source":{"layout":"flat","absolute_path":"C:/synthetic/payload","signature_path":"C:/synthetic/payload.sig"}}`,
		`{"schema":1,"operation_id":"so1_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","action":"apply_pending","expected_payload_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`,
		`{"schema":1,"operation_id":"so1_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","action":"remove","keep_profiles":true}`,
	}
	for _, input := range good {
		if _, err := DecodeApplyRequest([]byte(input)); err != nil {
			t.Fatalf("valid request: %v", err)
		}
	}
	for _, input := range []string{
		strings.Replace(good[1], `"schema":1`, `"schema":1,"schema":1`, 1),
		strings.Replace(good[1], `"schema":1`, `"Schema":1`, 1),
		strings.Replace(good[1], `"schema":1`, `"schema":1,"source":null`, 1),
		strings.Replace(good[1], `"schema":1`, `"schema":1,"trust":""`, 1),
		strings.Replace(good[1], `"schema":1`, `"schema":1,"keep_profiles":false`, 1),
		strings.Replace(good[1], `"action":"apply_pending"`, `"action":"exec"`, 1),
		strings.Replace(good[1], `"schema":1`, `"schema":1e0`, 1),
		strings.Replace(good[1], `"schema":1,`, "", 1),
		strings.Replace(good[1], `"so1_`, `"it1_`, 1),
		strings.Replace(good[2], `"keep_profiles":true`, `"keep_profiles":false`, 1),
		strings.Replace(good[0], `"layout":"flat"`, `"layout":"flat","layout":"flat"`, 1),
		strings.Replace(good[0], `"trust":"release"`, `"trust":"legacy_root_owned"`, 1),
		strings.Replace(good[0], `"absolute_path":`, `"AbsolutePath":`, 1),
		good[1] + `{}`, "null", "[]", strings.Repeat(" ", 65537),
	} {
		if _, err := DecodeApplyRequest([]byte(input)); err == nil {
			t.Fatalf("ambiguous request accepted: %s", input[:min(len(input), 100)])
		}
	}
}

func TestJournalStrictPhaseAuthority(t *testing.T) {
	runtime := testRuntime()
	j := Journal{Schema: 1, Generation: 7, Manager: "standalone", Active: &runtime,
		Transaction: &Transaction{ID: "it1_" + strings.Repeat("c", 32), Action: "install", Phase: "committed_open", OperationalCommitID: "oc1_" + strings.Repeat("d", 32), Trust: "release", Candidate: &runtime,
			Checkpoint:    &CheckpointRef{ID: "checkpoint_fixture", Revision: 40, SHA256: strings.Repeat("e", 64)},
			SchemaReceipt: &SchemaReceipt{MigrationID: "migration_fixture", Schema: 2, Revision: 41, CanonicalSHA256: strings.Repeat("f", 64)}}}
	data, err := EncodeJournal(j)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeJournal(data); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Journal){
		"postcommit-no-id":         func(j *Journal) { j.Transaction.OperationalCommitID = "" },
		"postcommit-no-receipt":    func(j *Journal) { j.Transaction.SchemaReceipt = nil },
		"postcommit-no-checkpoint": func(j *Journal) { j.Transaction.Checkpoint = nil },
		"postcommit-other-active":  func(j *Journal) { j.Active = nil },
		"precommit-with-id":        func(j *Journal) { j.Transaction.Phase = "ready" },
		"removed-active":           func(j *Journal) { j.Transaction.Action = "remove"; j.Transaction.Phase = "removed" },
		"unknown-phase":            func(j *Journal) { j.Transaction.Phase = "running" },
		"unknown-manager":          func(j *Journal) { j.Manager = "custom" },
		"malformed-id":             func(j *Journal) { j.Transaction.ID = "it1_../" },
		"zero-generation":          func(j *Journal) { j.Generation = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			copy, _ := DecodeJournal(data)
			mutate(&copy)
			if _, err := EncodeJournal(copy); err == nil {
				t.Fatal("invalid authority encoded")
			}
		})
	}
	for _, input := range [][]byte{
		bytes.Replace(data, []byte(`"schema": 1`), []byte(`"schema": 1, "schema": 1`), 1),
		bytes.Replace(data, []byte(`"schema": 1`), []byte(`"Schema": 1`), 1),
		bytes.Replace(data, []byte(`"schema": 1`), []byte(`"schema": null`), 1),
		append(data, []byte("{}")...), []byte("null"),
	} {
		if _, err := DecodeJournal(input); err == nil {
			t.Fatal("ambiguous journal decoded")
		}
	}
}

func TestManifestCanonicalRolesAndArtifactIdentity(t *testing.T) {
	m := Manifest{Schema: 1, Kind: "tenebra-install-payload", Version: "0.6.0", SourceSHA: strings.Repeat("a", 40), Target: "linux-amd64"}
	for _, name := range []string{"tenebra-core", "sing-box", "geosite-ru.srs", "geoip-ru.srs", "geosite-ads.srs"} {
		mode := uint32(0644)
		if name == "tenebra-core" || name == "sing-box" {
			mode = 0755
		}
		m.Files = append(m.Files, ManifestFile{Name: name, SHA256: strings.Repeat("b", 64), Size: 17, Mode: mode})
	}
	data, err := CanonicalManifest(m)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, []byte("{\n  \"schema\": 1,\n  \"kind\": ")) || data[len(data)-1] != '\n' {
		t.Fatal("noncanonical manifest framing")
	}
	var decoded Manifest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Files[0].Name != "geoip-ru.srs" || decoded.Files[4].Name != "tenebra-core" {
		t.Fatal("roles not sorted")
	}
	reversed := m
	reversed.Files = append([]ManifestFile{}, m.Files...)
	for a, b := 0, len(reversed.Files)-1; a < b; a, b = a+1, b-1 {
		reversed.Files[a], reversed.Files[b] = reversed.Files[b], reversed.Files[a]
	}
	other, err := CanonicalManifest(reversed)
	if err != nil || !bytes.Equal(other, data) {
		t.Fatal("input order changed artifact identity")
	}
	for _, name := range []string{"../tenebra-core", "helper", "payload.json", "sub/core", "geoip-ru.srs"} {
		bad := m
		bad.Files = append(append([]ManifestFile{}, m.Files...), ManifestFile{Name: name, SHA256: strings.Repeat("c", 64), Size: 1, Mode: 0644})
		if _, err := CanonicalManifest(bad); err == nil {
			t.Fatalf("extra/unsafe/duplicate role accepted: %s", name)
		}
	}
}
