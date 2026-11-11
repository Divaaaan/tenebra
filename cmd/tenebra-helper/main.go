package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/Divaaaan/tenebra/internal/installtx"
	"github.com/Divaaaan/tenebra/internal/securefs"
)

type commandRunner func(context.Context, installtx.HelperCommand) (installtx.Result, error)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Geteuid(), installtx.RunNativeHelperCommand))
}
func run(ctx context.Context, args []string, input io.Reader, out, diagnostics io.Writer, effectiveUID int, runner commandRunner) int {
	command, err := installtx.ParseHelperCommand(args, input, effectiveUID)
	if err != nil {
		return fail(diagnostics, err)
	}
	result, err := runner(ctx, command)
	if err != nil {
		return fail(diagnostics, err)
	}
	if command.Name == "service-host" {
		return 0
	}
	encoder := json.NewEncoder(out)
	if command.Name == "status" {
		body, err := installtx.EncodePublicStatus(result.Status)
		if err != nil {
			return fail(diagnostics, err)
		}
		if _, err := out.Write(body); err != nil {
			return 1
		}
		return 0
	}
	if _, err := installtx.EncodePublicStatus(result.Status); err != nil {
		return fail(diagnostics, err)
	}
	frame := struct {
		Status  installtx.Status `json:"status"`
		Changed bool             `json:"changed"`
	}{result.Status, result.Changed}
	if err := encoder.Encode(frame); err != nil {
		return 1
	}
	return 0
}
func fail(out io.Writer, err error) int {
	code := "operation_failed"
	for _, item := range []struct {
		err  error
		code string
	}{
		{installtx.ErrHelperPrivilege, "root_required"}, {installtx.ErrHelperAckRequired, "untrusted_input_ack_required"}, {installtx.ErrProtocol, "invalid_request"},
		{installtx.ErrSignature, "signature_invalid"}, {installtx.ErrConflict, "install_conflict"}, {installtx.ErrReadiness, "readiness_refused"},
		{installtx.ErrLegacyAdoptionRequired, "legacy_adoption_required"}, {installtx.ErrUnknownService, "unknown_service_preserved"},
		{installtx.ErrUntrustedHelperRole, "untrusted_installed_helper"},
		{securefs.ErrUnsupported, "unsupported_platform"}, {securefs.ErrUntrusted, "untrusted_object"}, {securefs.ErrChanged, "object_changed"},
		{installtx.ErrRecoveryRequired, "recovery_required"}, {context.DeadlineExceeded, "operation_pending"}, {context.Canceled, "cancelled"},
	} {
		if errors.Is(err, item.err) {
			code = item.code
			break
		}
	}
	_ = json.NewEncoder(out).Encode(struct {
		Error string `json:"error"`
	}{code})
	return 1
}
