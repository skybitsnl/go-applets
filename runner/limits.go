package runner

// Limits bounds the resources of a run. The time of a run is bounded by the
// context passed to Run.
type Limits struct {
	noCloseOnContextDone bool
	memoryMiB            uint32
}

// WithCloseOnContextDone sets whether cancelling the context passed to Run
// stops the execution. The default is true. If you set this to false, the
// performance improves, but the sandbox can continue running forever, so be
// aware that this breaks the security model.
func (l Limits) WithCloseOnContextDone(d bool) Limits {
	l.noCloseOnContextDone = !d
	return l
}

// WithMemoryMiB bounds each linear memory of an applet and its dependencies.
// The default is 64 MiB, and 4096 MiB is the WASM limit.
func (l Limits) WithMemoryMiB(mebibytes uint32) Limits {
	l.memoryMiB = min(mebibytes, 4096)
	return l
}

func (l Limits) withDefaults() Limits {
	if l.memoryMiB == 0 {
		l.memoryMiB = 64
	}

	return l
}
