package installtx

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"

	"github.com/Divaaaan/tenebra/internal/securefs"
)

func verifyRuntimeLease(ctx context.Context, lease *securefs.PathLease, manifest Manifest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	canonical, err := CanonicalManifest(manifest)
	if err != nil {
		return err
	}
	names := lease.Files()
	if len(names) != len(manifest.Files)+1 {
		return ErrProtocol
	}
	expected := map[string]bool{"payload.json": true}
	for _, file := range manifest.Files {
		expected[file.Name] = true
	}
	for _, name := range names {
		if !expected[name] {
			return ErrProtocol
		}
	}
	body, err := lease.ReadFile("payload.json", MaxMetadataBytes)
	if err != nil {
		return err
	}
	if !bytes.Equal(body, canonical) {
		return ErrProtocol
	}
	for _, file := range manifest.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		reader, err := lease.OpenFile(file.Name, file.Size)
		if err != nil {
			return err
		}
		hash := sha256.New()
		size, readErr := io.Copy(hash, contextReader{ctx: ctx, Reader: reader})
		closeErr := reader.Close()
		if readErr != nil || closeErr != nil {
			return errors.Join(readErr, closeErr)
		}
		if size != file.Size || hex.EncodeToString(hash.Sum(nil)) != file.SHA256 {
			return ErrProtocol
		}
	}
	return nil
}
