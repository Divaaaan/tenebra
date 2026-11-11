package installtx

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/Divaaaan/tenebra/internal/securefs"
)

func testProcessRecord(t *testing.T) *ProcessRecordFile {
	t.Helper()
	f, err := openProcessRecordFile(privateInstallerRoot(t), securefs.Policy{Mode: securefs.SameAccountPrivate})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.Close(); err != nil {
			t.Error(err)
		}
	})
	return f
}
func TestProcessRecordCASDoesNotOverwriteOtherLaunchOrAliasCaller(t *testing.T) {
	ctx := context.Background()
	f := testProcessRecord(t)
	p := hostedFixture(installerDisk().journal)
	empty, err := f.Read(ctx)
	if err != nil || empty.Generation != 0 || empty.Current != nil {
		t.Fatal(empty, err)
	}
	saved, err := f.Replace(ctx, 0, &p)
	if err != nil || saved.Generation != 1 {
		t.Fatal(saved, err)
	}
	saved.Current.LaunchID = strings.Repeat("a", 32)
	p.Ready.StoreRevision = 999
	again, err := f.Read(ctx)
	if err != nil || again.Current.LaunchID == saved.Current.LaunchID || again.Current.Ready.StoreRevision == 999 {
		t.Fatal("caller changed persisted metadata", again, err)
	}
	if _, err := f.Replace(ctx, 0, nil); !errors.Is(err, ErrConflict) {
		t.Fatal("stale launch overwrote process record", err)
	}
	if cleared, err := f.Replace(ctx, 1, nil); err != nil || cleared.Generation != 2 || cleared.Current != nil {
		t.Fatal(cleared, err)
	}
}

func TestProcessRecordConcurrentHostsHaveOneCASWinnerAndReadDoesNotWait(t *testing.T) {
	ctx := context.Background()
	f := testProcessRecord(t)
	p := hostedFixture(installerDisk().journal)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; _, err := f.Replace(ctx, 0, &p); results <- err }()
	}
	close(start)
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, securefs.ErrBusy) && !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatal("multiple host launch records won the same generation", success)
	}
	lock, err := f.root.LockWriter(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if record, err := f.Read(ctx); err != nil || record.Generation != 1 {
		t.Fatal("read-only service lookup waited on writer", record, err)
	}
	if _, err := f.Replace(ctx, 1, nil); !errors.Is(err, securefs.ErrBusy) {
		t.Fatal("second writer crossed metadata lock", err)
	}
}

func TestProcessRecordMalformedStateIsPreservedAndNeverHealed(t *testing.T) {
	p := hostedFixture(installerDisk().journal)
	good, _ := json.Marshal(ProcessRecord{Schema: 1, Generation: 1, Current: &p})
	for name, body := range map[string]string{
		"duplicate":         strings.Replace(string(good), `"schema":1`, `"schema":1,"schema":1`, 1),
		"unknown-key":       strings.Replace(string(good), `"schema":1`, `"schema":1,"exec":"/tmp/core"`, 1),
		"case":              strings.Replace(string(good), `"schema":1`, `"Schema":1`, 1),
		"generation-zero":   strings.Replace(string(good), `"generation":1`, `"generation":0`, 1),
		"null":              `null`,
		"wrong-ready-owner": strings.Replace(string(good), `"launch_id":"`+p.LaunchID+`"`, `"launch_id":"`+strings.Repeat("b", 32)+`"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			f := testProcessRecord(t)
			ctx := context.Background()
			if state, err := f.root.ReplaceFile("current.json", []byte(body)); err != nil || state != securefs.Committed {
				t.Fatal(state, err)
			}
			if _, err := f.Read(ctx); !errors.Is(err, ErrRecoveryRequired) {
				t.Fatal("ambiguous process record accepted", err)
			}
			if _, err := f.Replace(ctx, 0, nil); !errors.Is(err, ErrRecoveryRequired) {
				t.Fatal("malformed process record auto-healed", err)
			}
			after, err := f.root.ReadFile("current.json", MaxMetadataBytes)
			if err != nil || string(after) != body {
				t.Fatal("forensic process metadata overwritten", err)
			}
		})
	}
}
