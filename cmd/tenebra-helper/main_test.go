package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Divaaaan/tenebra/internal/installtx"
)

func helperResult() installtx.Result {
	return installtx.Result{Changed: true, Status: installtx.Status{Phase: "committed_open", Manager: "standalone", ActiveVersion: "0.6.0", TransactionID: "it1_" + strings.Repeat("a", 32), OperationalCommitID: "oc1_" + strings.Repeat("b", 32), BusinessAdmission: "open"}}
}
func TestHelperExecutableEmitsBoundedP0ResultWithSnakeCase(t *testing.T) {
	var out, diagnostics bytes.Buffer
	calls := 0
	body := `{"schema":1,"operation_id":"so1_` + strings.Repeat("a", 32) + `","action":"apply_pending","expected_payload_sha256":"` + strings.Repeat("b", 64) + `"}`
	code := run(context.Background(), []string{"apply", "--request-stdin"}, strings.NewReader(body), &out, &diagnostics, 0, func(_ context.Context, c installtx.HelperCommand) (installtx.Result, error) {
		calls++
		if c.Request.Action != "apply_pending" {
			t.Fatal(c)
		}
		return helperResult(), nil
	})
	if code != 0 || calls != 1 || diagnostics.Len() != 0 {
		t.Fatal(code, calls, diagnostics.String())
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &fields); err != nil || len(fields) != 2 || fields["status"] == nil || fields["changed"] == nil {
		t.Fatal("helper result schema drifted", out.String(), err)
	}
}
func TestHelperExecutableUnprivilegedStatusContainsOnlyPublicDTO(t *testing.T) {
	var out, diagnostics bytes.Buffer
	code := run(context.Background(), []string{"status", "--json"}, strings.NewReader("ignored"), &out, &diagnostics, 1000, func(context.Context, installtx.HelperCommand) (installtx.Result, error) { return helperResult(), nil })
	if code != 0 || diagnostics.Len() != 0 {
		t.Fatal(code, diagnostics.String())
	}
	if _, err := installtx.DecodePublicStatus(out.Bytes()); err != nil || bytes.Contains(out.Bytes(), []byte(`"status":`)) {
		t.Fatal("public status wrapped private result", out.String(), err)
	}
}
func TestHelperExecutableRefusesUnprivilegedMutationBeforeNativeRunner(t *testing.T) {
	var out, diagnostics bytes.Buffer
	calls := 0
	code := run(context.Background(), []string{"service-host"}, strings.NewReader("ignored"), &out, &diagnostics, 1000, func(context.Context, installtx.HelperCommand) (installtx.Result, error) {
		calls++
		return helperResult(), nil
	})
	if code != 1 || calls != 0 || out.Len() != 0 || diagnostics.String() != "{\"error\":\"root_required\"}\n" {
		t.Fatal(code, calls, out.String(), diagnostics.String())
	}
}
func TestHelperExecutableNeverPrintsRawNativeErrors(t *testing.T) {
	var out, diagnostics bytes.Buffer
	code := run(context.Background(), []string{"service-host"}, strings.NewReader("ignored"), &out, &diagnostics, 0, func(context.Context, installtx.HelperCommand) (installtx.Result, error) {
		return installtx.Result{}, errors.New("synthetic-private-path/token/password")
	})
	if code != 1 || out.Len() != 0 || diagnostics.String() != "{\"error\":\"operation_failed\"}\n" {
		t.Fatal("native error leaked", code, out.String(), diagnostics.String())
	}
}
