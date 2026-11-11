package installtx

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type helperInputCounter struct {
	reader       io.Reader
	calls, bytes int
}

func (r *helperInputCounter) Read(p []byte) (int, error) {
	r.calls++
	n, err := r.reader.Read(p)
	r.bytes += n
	return n, err
}

func helperRequestBody(t *testing.T, action, trust string) []byte {
	t.Helper()
	r := ApplyRequest{Schema: 1, OperationID: "so1_" + strings.Repeat("a", 32), Action: action}
	switch action {
	case "install":
		r.Trust = trust
		r.ExpectedVersion = "0.6.0"
		r.ExpectedSourceSHA = strings.Repeat("b", 40)
		r.ExpectedPayloadSHA256 = strings.Repeat("c", 64)
		r.Source = &Source{Layout: "flat", AbsolutePath: filepath.Join(os.TempDir(), "synthetic-payload")}
		if trust == "release" {
			r.Source.SignaturePath = filepath.Join(os.TempDir(), "synthetic-payload.minisig")
		}
	case "apply_pending":
		r.ExpectedPayloadSHA256 = strings.Repeat("c", 64)
	case "remove":
		r.KeepProfiles = true
	}
	body, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestHelperCommandChecksKernelPrivilegeBeforeReadingStdin(t *testing.T) {
	for _, args := range [][]string{{"apply", "--request-stdin"}, {"remove", "--keep-profiles", "--request-stdin"}, {"stage-package", "--manager=dpkg"}, {"recover", "--transaction=it1_" + strings.Repeat("a", 32)}, {"service-host"}} {
		input := &helperInputCounter{reader: strings.NewReader("private stdin must not be touched")}
		if _, err := ParseHelperCommand(args, input, 1000); !errors.Is(err, ErrHelperPrivilege) || input.calls != 0 {
			t.Fatal("unprivileged command read stdin", args, input.calls, err)
		}
	}
	input := &helperInputCounter{reader: strings.NewReader("ignored")}
	if c, err := ParseHelperCommand([]string{"status", "--json"}, input, 1000); err != nil || c.Name != "status" || input.calls != 0 {
		t.Fatal(c, err, input.calls)
	}
}

func TestHelperCommandAcceptsOnlyExactNonRequestCommands(t *testing.T) {
	tx := "it1_" + strings.Repeat("a", 32)
	for _, args := range [][]string{{"service-host"}, {"recover", "--transaction=" + tx}, {"stage-package", "--manager=dpkg"}, {"stage-package", "--manager=pacman"}, {"status", "--json"}} {
		input := &helperInputCounter{reader: strings.NewReader("ignored")}
		c, err := ParseHelperCommand(args, input, 0)
		if err != nil || input.calls != 0 || c.Name != args[0] {
			t.Fatal(args, c, err, input.calls)
		}
		if c.Name == "recover" && c.TransactionID != tx {
			t.Fatal("transaction argument lost")
		}
	}
}

func TestHelperCommandRejectsArbitraryExecutionAndDuplicateFlagsBeforeInput(t *testing.T) {
	for _, args := range [][]string{
		{}, {"exec", "/bin/sh"}, {"service-host", "--socket=/other"}, {"service-host", "--env=TENEBRA_CONFIG_DIR=x"},
		{"status"}, {"status", "--json", "--json"}, {"status", "--json", "--exec"},
		{"recover", "--transaction=it1_" + strings.Repeat("A", 32)}, {"recover", "--transaction=it1_" + strings.Repeat("a", 32), "--force"},
		{"stage-package", "--manager=apt"}, {"stage-package", "--manager=dpkg", "--destination=/tmp"},
		{"apply", "--request-stdin", "--request-stdin"}, {"apply", "--request-stdin", "--label=foreign"},
		{"apply", "--request-stdin", "--allow-unsafe-source"}, {"apply", "--from-dir=/tmp"},
		{"remove", "--request-stdin"}, {"remove", "--keep-profiles", "--request-stdin", "--ack-untrusted-input"},
	} {
		input := &helperInputCounter{reader: strings.NewReader("must not be read")}
		if _, err := ParseHelperCommand(args, input, 0); err == nil || input.calls != 0 {
			t.Fatal("invalid CLI read input or was accepted", args, input.calls, err)
		}
	}
}

func TestHelperCommandStrictRequestUnionAndExplicitDevAck(t *testing.T) {
	for _, item := range []struct {
		args          []string
		action, trust string
		want          error
	}{
		{[]string{"apply", "--request-stdin"}, "install", "release", nil},
		{[]string{"apply", "--request-stdin", "--ack-untrusted-input"}, "install", "operator_dev", nil},
		{[]string{"apply", "--request-stdin"}, "install", "operator_dev", ErrHelperAckRequired},
		{[]string{"apply", "--request-stdin", "--ack-untrusted-input"}, "install", "release", ErrProtocol},
		{[]string{"apply", "--request-stdin"}, "apply_pending", "", nil},
		{[]string{"remove", "--keep-profiles", "--request-stdin"}, "remove", "", nil},
		{[]string{"apply", "--request-stdin"}, "remove", "", ErrProtocol},
		{[]string{"remove", "--keep-profiles", "--request-stdin"}, "install", "release", ErrProtocol},
	} {
		c, err := ParseHelperCommand(item.args, strings.NewReader(string(helperRequestBody(t, item.action, item.trust))), 0)
		if !errors.Is(err, item.want) || err == nil && (c.Request == nil || c.Request.Action != item.action) {
			t.Fatal(item.args, c, err, item.want)
		}
	}
	valid := helperRequestBody(t, "install", "release")
	var value map[string]any
	if err := json.Unmarshal(valid, &value); err != nil {
		t.Fatal(err)
	}
	value["operator_principal"] = "uid:0"
	forged, _ := json.Marshal(value)
	if _, err := ParseHelperCommand([]string{"apply", "--request-stdin"}, strings.NewReader(string(forged)), 0); err == nil {
		t.Fatal("request supplied authenticated principal")
	}
	value = map[string]any{}
	if err := json.Unmarshal(valid, &value); err != nil {
		t.Fatal(err)
	}
	value["source"].(map[string]any)["absolute_path"] = "relative-payload"
	forged, _ = json.Marshal(value)
	if _, err := ParseHelperCommand([]string{"apply", "--request-stdin"}, strings.NewReader(string(forged)), 0); err == nil {
		t.Fatal("relative source path accepted")
	}
}

func TestHelperCommandBoundsStdinBeforeDecode(t *testing.T) {
	input := &helperInputCounter{reader: strings.NewReader(strings.Repeat("x", MaxMetadataBytes+2000))}
	if _, err := ParseHelperCommand([]string{"apply", "--request-stdin"}, input, 0); !errors.Is(err, ErrProtocol) || input.bytes != MaxMetadataBytes+1 {
		t.Fatal("stdin was unbounded", input.bytes, err)
	}
	for _, body := range []string{"", `{"schema":1,"schema":1}`, `{"schema":null}`, string(helperRequestBody(t, "apply_pending", "")) + "{}"} {
		if _, err := ParseHelperCommand([]string{"apply", "--request-stdin"}, strings.NewReader(body), 0); err == nil {
			t.Fatal("malformed request accepted")
		}
	}
}
