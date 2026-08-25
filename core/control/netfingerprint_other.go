//go:build !windows

package control

// networkIdentity is the stub off Windows: it reports that the network cannot be
// identified, so networkFingerprint returns "" and the per-network strategy
// memory stays out of the way.
//
// The thing it keys is Windows-only to begin with. A bypass strategy is a winws
// process on the WinDivert driver, and core/zapret's non-Windows Runner refuses
// to start one (see run_other.go), so there is never a measurement to file here.
// The type exists on every platform for the same reason that Runner does: the
// control layer is platform-neutral and calls it unconditionally, and a function
// present in only one build breaks the build and the vet in every other.
//
// Porting the bypass would mean porting this alongside it. The shape the Windows
// implementation settled on is the default gateway's hardware address, read out
// of the neighbour cache, which every platform this could reach has an
// equivalent of.
func networkIdentity() string { return "" }
