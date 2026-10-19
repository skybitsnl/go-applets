package build

import (
	"fmt"
	"strings"
)

// SourceError is one problem in the applet source, such as a type error or a
// syntax error.
type SourceError struct {
	// If zero: not on a particular line. Otherwise, this is the 1-indexed line number.
	Line int
	// The 1-indexed column number.
	Column int
	// If the error is a typecheck error, Code contains the tsc error code, e.g. "TS2332".
	Code    string
	Message string
}

// String formats the error as "line:column: code: message", leaving out what
// it does not have.
func (e SourceError) String() string {
	message := e.Message
	if e.Code != "" {
		message = e.Code + ": " + message
	}
	if e.Line == 0 {
		return message
	}
	return fmt.Sprintf("%d:%d: %s", e.Line, e.Column, message)
}

// Step is a step of building an applet.
type Step int

const (
	StepTypecheck Step = iota + 1 // Builder.Check
	StepBundle                    // Bundle
	StepCompile                   // Builder.Compile
)

// InvalidSourceError is returned when the applet source is invalid. Step is
// the step that found out, and Errors are the problems it found, in the order
// the step reported them.
type InvalidSourceError struct {
	Step   Step
	Errors []SourceError
}

func (e *InvalidSourceError) Error() string {
	b := strings.Builder{}
	switch e.Step {
	case StepTypecheck:
		b.WriteString("type check failed: ")
	case StepBundle:
		b.WriteString("failed to bundle applet: ")
	case StepCompile:
		b.WriteString("failed to compile applet: ")
	}

	for i, d := range e.Errors {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(d.String())
	}
	return b.String()
}
