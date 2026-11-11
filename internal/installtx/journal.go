package installtx

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"math"

	"github.com/Divaaaan/tenebra/internal/securefs"
)

// JournalFile uses two independent kernel locks. A long-running transaction
// never blocks service-host's protected atomic journal read, nor holds the
// canonical data repository's writer lock while copying an input package.
type JournalFile struct{ root, transactions *securefs.Root }

func OpenJournalFile(root *securefs.Root) (*JournalFile, error) {
	if root == nil {
		return nil, ErrProtocol
	}
	tx, err := root.OpenDir("transactions", securefs.Policy{Mode: securefs.MachinePrivate}, true)
	if err != nil {
		return nil, err
	}
	return &JournalFile{root: root, transactions: tx}, nil
}

// OpenAccountJournalFile is for explicitly isolated, non-service test fixtures.
// Production helpers must use OpenJournalFile's machine-only namespace.
func openAccountJournalFile(root *securefs.Root) (*JournalFile, error) {
	if root == nil {
		return nil, ErrProtocol
	}
	tx, err := root.OpenDir("transactions", securefs.Policy{Mode: securefs.SameAccountPrivate}, true)
	if err != nil {
		return nil, err
	}
	return &JournalFile{root: root, transactions: tx}, nil
}
func (f *JournalFile) Close() error {
	if f == nil || f.transactions == nil {
		return nil
	}
	return f.transactions.Close()
}
func (f *JournalFile) LockTransaction(ctx context.Context) (io.Closer, error) {
	if f == nil || f.transactions == nil {
		return nil, ErrProtocol
	}
	return f.transactions.LockWriter(ctx)
}
func (f *JournalFile) ReadJournal(ctx context.Context) (Journal, error) {
	if f == nil || f.root == nil {
		return Journal{}, ErrProtocol
	}
	if err := ctx.Err(); err != nil {
		return Journal{}, err
	}
	data, err := f.root.ReadAtomicFile(ctx, "journal.json", MaxMetadataBytes)
	if err != nil {
		return Journal{}, err
	}
	return DecodeJournal(data)
}
func (f *JournalFile) ReplaceJournal(ctx context.Context, candidate Journal) error {
	data, err := EncodeJournal(candidate)
	if err != nil {
		return err
	}
	lock, err := f.root.LockWriter(ctx)
	if err != nil {
		return err
	}
	defer lock.Close()
	current, err := f.ReadJournal(ctx)
	if errors.Is(err, fs.ErrNotExist) {
		if candidate.Generation != 1 {
			return ErrConflict
		}
	} else if err != nil {
		return err
	} else if current.Generation == math.MaxUint64 || candidate.Generation != current.Generation+1 {
		return ErrConflict
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	outcome, err := f.root.ReplaceFile("journal.json", data)
	if outcome != securefs.Committed || err != nil {
		if outcome != securefs.Unchanged {
			return errors.Join(ErrRecoveryRequired, err)
		}
		if err == nil {
			return ErrProtocol
		}
		return err
	}
	return nil
}
