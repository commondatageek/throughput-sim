// Package logx is a tiny leveled logger for the forecast CLI: plain,
// human-readable lines ("<LEVEL> <message>") instead of structured
// key=value output, with the level color-coded when stderr is a terminal
// and NO_COLOR is unset.
package logx

import (
	"fmt"
	"os"
	"sync"

	"github.com/mattn/go-isatty"
)

// Level is a logging severity.
type Level int

const (
	Debug Level = iota
	Info
	Warn
	Error
)

var levelLabels = map[Level]string{
	Debug: "DEBUG",
	Info:  "INFO ",
	Warn:  "WARN ",
	Error: "ERROR",
}

var levelColors = map[Level]string{
	Debug: "\033[36m", // cyan
	Info:  "\033[32m", // green
	Warn:  "\033[33m", // yellow
	Error: "\033[31m", // red
}

const colorReset = "\033[0m"

var (
	mu        sync.Mutex
	threshold = Info
	colorOn   = isatty.IsTerminal(os.Stderr.Fd()) && os.Getenv("NO_COLOR") == ""
)

// SetLevel sets the minimum level that will be logged. The default is Info.
func SetLevel(l Level) {
	mu.Lock()
	defer mu.Unlock()
	threshold = l
}

func logf(l Level, format string, args ...any) {
	mu.Lock()
	defer mu.Unlock()
	if l < threshold {
		return
	}
	msg := fmt.Sprintf(format, args...)
	if colorOn {
		fmt.Fprintf(os.Stderr, "%s%s%s %s\n", levelColors[l], levelLabels[l], colorReset, msg)
	} else {
		fmt.Fprintf(os.Stderr, "%s %s\n", levelLabels[l], msg)
	}
}

// Debugf logs a debug-level message. Suppressed unless the threshold is
// lowered via SetLevel.
func Debugf(format string, args ...any) { logf(Debug, format, args...) }

// Infof logs an info-level message.
func Infof(format string, args ...any) { logf(Info, format, args...) }

// Warnf logs a warn-level message.
func Warnf(format string, args ...any) { logf(Warn, format, args...) }

// Errorf logs an error-level message.
func Errorf(format string, args ...any) { logf(Error, format, args...) }
