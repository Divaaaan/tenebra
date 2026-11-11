package securefs

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

const forensicMaxFiles = 4096
const forensicMaxBytes = int64(1 << 30)
const forensicArchiveMax = forensicMaxBytes + (64 << 20)

// Original objects remain untrusted. A private parent contains their namespace;
// a new private archive provides the recovery copy. Original ownership and ACLs
// are never repaired or treated as execution provenance.
func verifyForensicContainment(ops nativeOps, source, parent handle, policy Policy, actor string) error {
	ancestor, err := ops.inspect(parent)
	if err != nil {
		return err
	}
	if ancestor.kind != kindDirectory {
		return ErrUntrusted
	}
	if err := verifyObject(ancestor, policy, actor, false); err != nil {
		return err
	}
	info, err := ops.inspect(source)
	if err != nil {
		return err
	}
	if (info.kind != kindDirectory && info.kind != kindRegular) || (info.kind == kindRegular && info.links != 1) {
		return ErrUntrusted
	}
	return nil
}

type quarantineRecord struct {
	Version        int    `json:"version"`
	ID             string `json:"id"`
	State          string `json:"state"`
	Acknowledged   bool   `json:"acknowledged"`
	SourceName     string `json:"source_name"`
	SourceObjectID string `json:"source_object_id"`
	SourceOwner    string `json:"source_owner"`
	OriginalBucket string `json:"original_bucket"`
	RecoveryCopy   string `json:"recovery_copy,omitempty"`
	ArchiveSHA256  string `json:"archive_sha256,omitempty"`
	Trust          string `json:"trust"`
}

func storeQuarantineRecord(vault *Root, record quarantineRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	state, err := vault.ReplaceFile("quarantine-"+record.ID+".json", data)
	if err != nil {
		return err
	}
	if state != Committed {
		return ErrChanged
	}
	return nil
}

func (r *Root) Quarantine(rel string, vault *Root) (receipt Receipt, result error) {
	if r == nil || r.state == nil || vault == nil || vault.state == nil {
		return receipt, ErrClosed
	}
	parts, err := relativeParts(rel)
	if err != nil {
		return receipt, err
	}
	if hasSealComponent(parts) {
		return receipt, ErrUnsupported
	}
	if len(parts) > 1 {
		parent, err := r.OpenDir(strings.Join(parts[:len(parts)-1], "/"), r.state.policy, false)
		if err != nil {
			return receipt, err
		}
		defer parent.Close()
		return parent.Quarantine(parts[len(parts)-1], vault)
	}
	s := r.state
	if s.objectControl == vault.state.objectControl || s.ops == nil {
		return receipt, ErrUntrusted
	}
	writer, err := vault.lockWriterNamed(context.Background(), ".quarantine-writer.lock")
	if err != nil {
		return receipt, err
	}
	defer func() { result = errors.Join(result, writer.Close()) }()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return receipt, ErrClosed
	}
	if s.policy.Mode == OperatorInput || s.frozen || s.readOnlyBinding {
		s.mu.Unlock()
		return receipt, ErrUnsupported
	}
	if s.publishing || s.leases > 0 || s.children > 0 {
		s.mu.Unlock()
		return receipt, ErrBusy
	}
	parentInfo, err := s.ops.inspect(s.h)
	if err == nil {
		err = verifyObject(parentInfo, s.policy, s.actor, true)
	}
	if err != nil || parentInfo.sticky {
		s.mu.Unlock()
		if err != nil {
			return receipt, err
		}
		return receipt, ErrUntrusted
	}
	if verifyObject(parentInfo, s.policy, s.actor, false) == nil {
		sealed, err := readSeal(s.ops, s.h, s.policy, s.actor)
		if err != nil || sealed {
			s.mu.Unlock()
			if err != nil {
				return receipt, err
			}
			return receipt, ErrUnsupported
		}
	}
	source, err := s.ops.openAt(s.h, parts[0], openRequest{any: true, delete: true})
	if err != nil {
		s.mu.Unlock()
		return receipt, err
	}
	info, err := s.ops.inspect(source)
	if err != nil {
		s.ops.close(source)
		s.mu.Unlock()
		return receipt, err
	}
	if info.kind == kindDirectory && verifyObject(info, s.policy, s.actor, false) == nil {
		sealed, err := readSeal(s.ops, source, s.policy, s.actor)
		if err != nil || sealed {
			s.ops.close(source)
			s.mu.Unlock()
			if err != nil {
				return receipt, err
			}
			return receipt, ErrUnsupported
		}
	}
	if (info.kind != kindDirectory && info.kind != kindRegular) || (info.kind == kindRegular && info.links != 1) {
		s.ops.close(source)
		s.mu.Unlock()
		return receipt, ErrUntrusted
	}
	s.publishing = true
	s.mu.Unlock()
	defer func() { s.ops.close(source); s.mu.Lock(); s.publishing = false; s.mu.Unlock() }()
	id, err := privateName("q_")
	if err != nil {
		return receipt, err
	}
	receipt.ID = id
	record := quarantineRecord{Version: 1, ID: id, State: "pending_detach", SourceName: rel, SourceObjectID: info.id, SourceOwner: info.owner,
		OriginalBucket: "quarantine-original-" + id, Trust: "forensic_untrusted"}
	// Reserve the ID first. A crash or uncertain write between the index and
	// the receipt is an incomplete recovery, never an empty/fresh install.
	if err := reserveQuarantineID(vault, id); err != nil {
		return receipt, err
	}
	if err := storeQuarantineRecord(vault, record); err != nil {
		return receipt, err
	}
	bucket, err := vault.OpenDir(record.OriginalBucket, vault.state.policy, true)
	if err != nil {
		return receipt, err
	}
	defer bucket.Close()
	s.mu.Lock()
	if s.leases > 0 || s.children > 0 {
		s.mu.Unlock()
		return receipt, ErrBusy
	}
	err = s.ops.rename(source, bucket.state.h, "original", false)
	s.mu.Unlock()
	if err != nil {
		return receipt, err
	}
	record.State = "detached_pending_copy"
	if err := storeQuarantineRecord(vault, record); err != nil {
		return receipt, err
	}
	if err := s.ops.containQuarantined(source, vault.state.policy); err != nil {
		return receipt, err
	}
	stage, err := vault.NewStage("recovery-stage-")
	if err != nil {
		return receipt, err
	}
	defer func() { result = errors.Join(result, stage.Close()) }()
	digest, err := archiveForensics(s.ops, source, stage.Root())
	if err != nil {
		return receipt, err
	}
	recoveryName := "recovery-" + id
	published, state, err := stage.Publish(context.Background(), recoveryName, Limits{MaxFiles: 2, MaxBytes: forensicArchiveMax}, func(ctx context.Context, lease *PathLease) error {
		reader, err := lease.OpenFile("payload.tar", forensicArchiveMax)
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, reader)
		closeErr := reader.Close()
		if err := errors.Join(copyErr, closeErr); err != nil {
			return err
		}
		if hex.EncodeToString(hash.Sum(nil)) != digest {
			return ErrChanged
		}
		return nil
	})
	if err != nil || state != Committed {
		if err == nil {
			err = ErrChanged
		}
		return receipt, err
	}
	if err := published.Close(); err != nil {
		return receipt, err
	}
	record.State = "recovery_required"
	record.RecoveryCopy = recoveryName
	record.ArchiveSHA256 = digest
	if err := storeQuarantineRecord(vault, record); err != nil {
		return receipt, err
	}
	return receipt, nil
}

type nativeArchiveWriter struct {
	ops     nativeOps
	h       handle
	written int64
}

func (w *nativeArchiveWriter) Write(buffer []byte) (int, error) {
	if int64(len(buffer)) > forensicArchiveMax-w.written {
		return 0, ErrLimit
	}
	count := 0
	for count < len(buffer) {
		n, err := w.ops.write(w.h, buffer[count:])
		if n < 0 || n > len(buffer)-count {
			return count, ErrChanged
		}
		count += n
		w.written += int64(n)
		if err != nil {
			return count, err
		}
		if n == 0 {
			return count, io.ErrShortWrite
		}
	}
	return count, nil
}

func archiveForensics(sourceOps nativeOps, source handle, destination *Root) (string, error) {
	dst := destination.state
	dst.mu.Lock()
	defer dst.mu.Unlock()
	if err := dst.ready(true); err != nil {
		return "", err
	}
	h, err := dst.ops.openAt(dst.h, "payload.tar", openRequest{write: true, create: true, policy: dst.policy})
	if err != nil {
		return "", err
	}
	defer dst.ops.close(h)
	output, err := dst.ops.inspect(h)
	if err != nil {
		return "", err
	}
	if err := verifyObject(output, dst.policy, dst.actor, false); err != nil {
		return "", err
	}
	dst.inventory.record("payload.tar", output.id)
	hash := sha256.New()
	writer := tar.NewWriter(io.MultiWriter(&nativeArchiveWriter{ops: dst.ops, h: h}, hash))
	var files int
	var total int64
	var walk func(handle, string, int) error
	walk = func(h handle, name string, depth int) error {
		files++
		if files > forensicMaxFiles || depth > 32 {
			return ErrLimit
		}
		info, err := sourceOps.inspect(h)
		if err != nil {
			return err
		}
		if info.kind != kindDirectory && info.kind != kindRegular {
			return ErrUntrusted
		}
		if info.kind == kindRegular && info.links != 1 {
			return ErrUntrusted
		}
		if info.kind == kindRegular && (info.size < 0 || info.size > forensicMaxBytes-total) {
			return ErrLimit
		}
		header := &tar.Header{Name: name, Mode: 0o600, Typeflag: tar.TypeReg, Size: info.size, Format: tar.FormatPAX}
		if info.kind == kindDirectory {
			header.Mode = 0o700
			header.Typeflag = tar.TypeDir
			header.Size = 0
			header.Name += "/"
		}
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		if info.kind == kindDirectory {
			names, err := sourceOps.list(h)
			if err != nil {
				return err
			}
			for _, childName := range names {
				if parts, err := relativeParts(childName); err != nil || len(parts) != 1 {
					return ErrUntrusted
				}
				child, err := sourceOps.openAt(h, childName, openRequest{any: true})
				if err != nil {
					return err
				}
				err = walk(child, name+"/"+childName, depth+1)
				sourceOps.close(child)
				if err != nil {
					return err
				}
			}
		} else {
			total += info.size
			remaining := info.size
			buffer := make([]byte, 32<<10)
			for remaining > 0 {
				chunk := buffer
				if remaining < int64(len(chunk)) {
					chunk = chunk[:remaining]
				}
				n, err := sourceOps.read(h, chunk)
				if n < 0 || n > len(chunk) {
					return ErrChanged
				}
				if n > 0 {
					if _, writeErr := writer.Write(chunk[:n]); writeErr != nil {
						return writeErr
					}
					remaining -= int64(n)
				}
				if err != nil {
					if errors.Is(err, io.EOF) && remaining == 0 {
						break
					}
					return err
				}
				if n == 0 {
					return io.ErrNoProgress
				}
			}
			var extra [1]byte
			n, err := sourceOps.read(h, extra[:])
			if n != 0 {
				return ErrChanged
			}
			if err != nil && !errors.Is(err, io.EOF) {
				return err
			}
		}
		after, err := sourceOps.inspect(h)
		if err != nil {
			return err
		}
		if info.id != after.id || info.change != after.change || info.size != after.size {
			return ErrChanged
		}
		return nil
	}
	if err := walk(source, "original", 0); err != nil {
		dst.poisoned = true
		return "", err
	}
	if err := writer.Close(); err != nil {
		dst.poisoned = true
		return "", err
	}
	if err := dst.ops.flush(h); err != nil {
		dst.poisoned = true
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
