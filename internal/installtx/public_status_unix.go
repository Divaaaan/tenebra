//go:build linux || darwin

package installtx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"

	"github.com/Divaaaan/tenebra/internal/securefs"
)

func ReadPublicStatus() (Status, error) {
	paths, err := SystemPaths(runtime.GOOS)
	if err != nil {
		return Status{}, err
	}
	return readPublicStatusAt(paths.Status)
}
func readPublicStatusAt(path string) (Status, error) {
	if !filepath.IsAbs(path) {
		return Status{}, ErrProtocol
	}
	root, err := securefs.OpenRoot(filepath.Dir(path), securefs.Policy{Mode: securefs.OperatorInput, Principal: "uid:0"}, false)
	if err != nil {
		return Status{}, err
	}
	body, readErr := root.ReadFile(filepath.Base(path), MaxMetadataBytes)
	if err := errors.Join(readErr, root.Close()); err != nil {
		return Status{}, err
	}
	return DecodePublicStatus(body)
}

func PublishPublicStatus(ctx context.Context, status Status) error {
	paths, err := SystemPaths(runtime.GOOS)
	if err != nil {
		return err
	}
	return publishPublicStatusAt(ctx, paths.Status, status)
}
func publishPublicStatusAt(ctx context.Context, path string, status Status) error {
	if os.Geteuid() != 0 || !filepath.IsAbs(path) {
		return ErrHelperPrivilege
	}
	body, err := EncodePublicStatus(status)
	if err != nil {
		return err
	}
	readRoot, err := securefs.OpenRoot(filepath.Dir(path), securefs.Policy{Mode: securefs.OperatorInput, Principal: "uid:0"}, false)
	if err != nil {
		return err
	}
	previous, readErr := readRoot.ReadFile(filepath.Base(path), MaxMetadataBytes)
	closeErr := readRoot.Close()
	if closeErr != nil {
		return closeErr
	}
	digest := ""
	if readErr == nil {
		// An unknown/malformed existing file is forensic evidence, not an
		// invitation to repair or replace it merely because the name matches.
		if _, err := DecodePublicStatus(previous); err != nil {
			return err
		}
		sum := sha256.Sum256(previous)
		digest = hex.EncodeToString(sum[:])
	} else if !errors.Is(readErr, fs.ErrNotExist) {
		return readErr
	}
	root, err := securefs.OpenRoot(filepath.Dir(path), securefs.Policy{Mode: securefs.MachineContainer}, false)
	if err != nil {
		return err
	}
	state, publishErr := root.PublishMachineMetadata(ctx, filepath.Base(path), body, digest, 0644)
	closeErr = root.Close()
	if state != securefs.Committed || publishErr != nil || closeErr != nil {
		return errors.Join(ErrRecoveryRequired, publishErr, closeErr)
	}
	return nil
}
