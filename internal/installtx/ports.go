package installtx

import "context"

type StorePort interface {
	Fence(context.Context, string) error
	Checkpoint(context.Context, string) (CheckpointRef, error)
	MigrateHeld(context.Context, string) (SchemaReceipt, error)
	RestoreHeld(context.Context, string, CheckpointRef) error
	Admit(context.Context, string, string) error
	Inspect(context.Context) (SchemaReceipt, error)
}
type FS interface {
	Stage(context.Context, Source, Manifest) (StagedPayload, error)
	ReadJournal(context.Context) (Journal, error)
	ReplaceJournal(context.Context, Journal) error
	PublishRuntime(context.Context, StagedPayload) (RuntimeRef, error)
	RemoveOwnedStage(context.Context, string) error
}
type ServiceManager interface {
	StopAndWait(context.Context) error
	StartHeld(context.Context, RuntimeRef, string, string) (ProcessIdentity, error)
	Admit(context.Context, string, string) error
}
