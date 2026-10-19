package runner

import (
	"errors"
	"fmt"
)

var (
	// ErrMemory is returned when the applet runs out of memory.
	ErrMemory = errors.New("applet exceeded its memory limit")
	// ErrInvalidOutput is returned when the applet writes a line to stdout
	// that is not a JSON object.
	ErrInvalidOutput = errors.New("applet wrote output that is not a JSON object")
	// ErrNotDone is returned when the applet stops without reporting that it
	// is done.
	ErrNotDone = errors.New("applet stopped without reporting that it is done")
	// ErrAfterDone is returned when the applet writes output after
	// reporting that it is done.
	ErrAfterDone = errors.New("applet wrote output after reporting that it is done")
)

// AppletError is returned when the applet fails.
type AppletError struct {
	Message string
	Stack   string
	// Input is the index of the input object the applet was processing, or -1
	// if it was not processing an input object.
	Input int
}

func (e *AppletError) Error() string {
	if e.Input >= 0 {
		return fmt.Sprintf("applet failed on input %d: %s", e.Input, e.Message)
	}
	return "applet failed: " + e.Message
}
