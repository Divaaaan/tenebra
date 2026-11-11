//go:build linux

package installtx

func newNativeServiceHostJob(journal *JournalFile) (ServiceHostJob, error) {
	return OpenLinuxServiceHostJob(journal)
}
