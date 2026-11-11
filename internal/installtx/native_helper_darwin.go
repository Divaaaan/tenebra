//go:build darwin

package installtx

import "github.com/Divaaaan/tenebra/internal/securefs"

func newNativeServiceHostJob(*JournalFile) (ServiceHostJob, error) {
	return nil, securefs.ErrUnsupported
}
