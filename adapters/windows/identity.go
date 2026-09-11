package windows

// ExecutablePath uses the same resolution as Start. The protection adapter
// validates this path and its ACL before granting it a persistent exception.
func (r *Runner) ExecutablePath() (string, error) { return r.resolveSingbox() }
