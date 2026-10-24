package ui

import (
	"fmt"
	"io"
	"sync"
	"time"
)

var (
	debugMu sync.Mutex
	debugW  io.Writer
)

// SetDebugLog enables a log of UI events (mouse, focus, layout). It never
// records message content.
func SetDebugLog(w io.Writer) {
	debugMu.Lock()
	debugW = w
	debugMu.Unlock()
}

func debugf(format string, args ...any) {
	debugMu.Lock()
	defer debugMu.Unlock()
	if debugW != nil {
		fmt.Fprintf(debugW, time.Now().Format("15:04:05.000 ")+format+"\n", args...)
	}
}
