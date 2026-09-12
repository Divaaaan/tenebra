//go:build !windows || (!amd64 && !arm64)

package protection

// Unsupported ABIs never use the 64-bit WFP structs and never claim protection.
func NewWindowsBackend(func() (string, error)) Backend { return nil }
