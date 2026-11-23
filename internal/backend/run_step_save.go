package backend

// saveRunStep saves history after each finished tool round, so a backend
// killed mid-run keeps the steps it completed. The run saves again when it
// ends, and a failure here resurfaces there.
func (rt *runtimeSession) saveRunStep() {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.recorder != nil {
		_ = rt.recorder.Save()
	}
}
