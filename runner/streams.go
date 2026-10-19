package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
)

// inputReader is the applet's stdin: the params, then one input object per
// line. It takes the next input object from next when the applet reads past
// the previous one.
type inputReader struct {
	next   func() (json.RawMessage, error, bool)
	cancel context.CancelCauseFunc

	buf   []byte
	count int
	eof   bool
}

func (r *inputReader) setLine(obj json.RawMessage) error {
	var b bytes.Buffer
	if err := json.Compact(&b, obj); err != nil {
		return err
	}
	b.WriteByte('\n')
	r.buf = b.Bytes()
	return nil
}

func (r *inputReader) Read(p []byte) (int, error) {
	if len(r.buf) == 0 {
		if r.eof {
			return 0, io.EOF
		}
		obj, err, ok := r.next()
		if !ok {
			r.eof = true
			return 0, io.EOF
		}
		if err != nil {
			r.cancel(err)
			return 0, err
		}
		if err := r.setLine(obj); err != nil {
			err = fmt.Errorf("invalid input object %d: %w", r.count, err)
			r.cancel(err)
			return 0, err
		}
		r.count++
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

// lineWriter splits what is written to it into lines, without the newline.
type lineWriter struct {
	emit func(line []byte) error
	line []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	n := len(p)
	for {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			w.line = append(w.line, p...)
			return n, nil
		}
		w.line = append(w.line, p[:i]...)
		p = p[i+1:]
		err := w.emit(w.line)
		w.line = w.line[:0]
		if err != nil {
			return 0, err
		}
	}
}

// outputWriter validates the lines of the applet's stdout and passes them to
// output.
type outputWriter struct {
	output func(json.RawMessage) error
	stderr *recordWriter
	cancel context.CancelCauseFunc
}

func (w *outputWriter) emit(line []byte) error {
	if w.stderr.done {
		w.cancel(ErrAfterDone)
		return ErrAfterDone
	}
	if len(line) == 0 || line[0] != '{' || !json.Valid(line) {
		w.cancel(ErrInvalidOutput)
		return ErrInvalidOutput
	}
	if err := w.output(json.RawMessage(bytes.Clone(line))); err != nil {
		w.cancel(err)
		return err
	}
	return nil
}

// recordWriter interprets the lines of the applet's stderr.
type recordWriter struct {
	log    func(string)
	cancel context.CancelCauseFunc

	done      bool
	appletErr *AppletError
	// lastRaw is the last line that is not a record, e.g. a message of a
	// crashing runtime, which may explain a trap.
	lastRaw string
}

// maxRawBytes bounds lastRaw.
const maxRawBytes = 4 << 10

func (w *recordWriter) emit(line []byte) error {
	if w.done {
		w.cancel(ErrAfterDone)
		return ErrAfterDone
	}
	var rec struct {
		Log   *string `json:"log"`
		Done  bool    `json:"done"`
		Error *string `json:"error"`
		Stack string  `json:"stack"`
		Input *int    `json:"input"`
	}
	if err := json.Unmarshal(line, &rec); err != nil || (rec.Log == nil && !rec.Done && rec.Error == nil) {
		w.lastRaw = string(line[:min(len(line), maxRawBytes)])
		w.logLine(w.lastRaw)
		return nil
	}
	switch {
	case rec.Error != nil:
		w.appletErr = &AppletError{Message: *rec.Error, Stack: rec.Stack, Input: -1}
		if rec.Input != nil {
			w.appletErr.Input = *rec.Input
		}
	case rec.Done:
		w.done = true
	case rec.Log != nil:
		w.logLine(*rec.Log)
	}
	return nil
}

func (w *recordWriter) logLine(s string) {
	if w.log != nil {
		w.log(s)
	}
}
