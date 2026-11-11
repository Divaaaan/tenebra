//go:build windows

package securefs

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestQuarantinePrivateCopyDoesNotInheritForeignChildACL(t *testing.T) {
	source, observed, path := nativeObservedRoot(t)
	if _, err := source.ReplaceFile("settings.json", []byte("INERT_UNTRUSTED_SETTINGS")); err != nil {
		t.Fatal(err)
	}
	actor, err := observed.principal()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;" + actor + ")(A;;FA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(filepath.Join(path, "settings.json"), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	observed.reads = 0
	if _, err := source.ReadFile("settings.json", 128); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("setup did not create untrusted data: %v", err)
	}
	if observed.reads != 0 {
		t.Fatal("untrusted settings reached payload reader")
	}
	vault, err := OpenRoot(privateFixtureDir(t), Policy{Mode: SameAccountPrivate}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer vault.Close()
	receipt, err := source.Quarantine("settings.json", vault)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ID == "" {
		t.Fatal("missing durable receipt identity")
	}
	if _, err := source.ReadFile("settings.json", 128); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("original remains an active setting: %v", err)
	}
	data, err := vault.ReadFile("quarantine-"+receipt.ID+".json", 16<<10)
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		State        string `json:"state"`
		Acknowledged bool   `json:"acknowledged"`
		RecoveryCopy string `json:"recovery_copy"`
	}
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if record.State != "recovery_required" || record.Acknowledged || record.RecoveryCopy == "" {
		t.Fatalf("quarantine was silently accepted: %+v", record)
	}
	recovery, err := vault.OpenDir(record.RecoveryCopy, Policy{Mode: SameAccountPrivate}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer recovery.Close()
	archive, err := recovery.ReadFile("payload.tar", 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	reader := tar.NewReader(bytes.NewReader(archive))
	header, err := reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	if header.Name != "original" || header.Mode != 0o600 {
		t.Fatalf("unsafe forensic archive entry: %+v", header)
	}
	content, err := io.ReadAll(reader)
	if err != nil || string(content) != "INERT_UNTRUSTED_SETTINGS" {
		t.Fatalf("forensic copy mismatch: %q,%v", content, err)
	}
}
