// Package keyfix rewrites modified-Enter key sequences that Bubble Tea v1
// cannot parse into one it can, before Bubble Tea reads the terminal.
//
// Terminals report Shift/Ctrl+Enter with the "CSI u" encoding (kitty keyboard
// protocol; foot as configured by Omarchy sends Shift+Return as ESC[13;2u via a
// text-binding) or xterm's modifyOtherKeys form (ESC[27;2;13~). Bubble Tea v1
// delivers those as an unknown-sequence message whose bytes alias its input
// buffer and are often overwritten by the next keystroke before the app sees
// them, so decoding them in Update is unreliable. Rewriting them here into
// ESC CR, which Bubble Tea parses as alt+enter, is reliable.
package keyfix

import (
	"bytes"
	"io"
	"os"
)

// AltEnter is what modified-Enter sequences are rewritten to.
var AltEnter = []byte("\x1b\r")

// sequences that mean "Enter with a modifier" (shift, alt, ctrl and
// combinations), in CSI-u and modifyOtherKeys form.
var sequences = func() [][]byte {
	var out [][]byte
	for _, mod := range []string{"2", "3", "4", "5", "6", "7", "8"} {
		out = append(out, []byte("\x1b[13;"+mod+"u"), []byte("\x1b[27;"+mod+";13~"))
	}
	return out
}()

// Translate rewrites every modified-Enter sequence in b to AltEnter.
func Translate(b []byte) []byte {
	if !bytes.Contains(b, []byte("\x1b[")) {
		return b
	}
	for _, seq := range sequences {
		if bytes.Contains(b, seq) {
			b = bytes.ReplaceAll(b, seq, AltEnter)
		}
	}
	return b
}

// File wraps the terminal. It keeps the *os.File's Fd, Write and Close so
// Bubble Tea still switches the real terminal to raw mode and its cancelable
// reader still polls the real descriptor; only Read is filtered.
type File struct {
	f       *os.File
	pending []byte // translated bytes that did not fit the caller's buffer
}

func Wrap(f *os.File) *File { return &File{f: f} }

func (w *File) Fd() uintptr                 { return w.f.Fd() }
func (w *File) Write(p []byte) (int, error) { return w.f.Write(p) }
func (w *File) Close() error                { return w.f.Close() }

// Read never splits a sequence on purpose: terminals write each key in one
// piece, so one read holds whole sequences. A sequence split across reads
// passes through untranslated (and is then ignored, as before).
func (w *File) Read(p []byte) (int, error) {
	if len(w.pending) > 0 {
		n := copy(p, w.pending)
		w.pending = w.pending[n:]
		return n, nil
	}
	buf := make([]byte, len(p))
	n, err := w.f.Read(buf)
	if n == 0 {
		return 0, err
	}
	out := Translate(buf[:n])
	c := copy(p, out)
	if c < len(out) {
		w.pending = append(w.pending[:0], out[c:]...)
	}
	return c, err
}

var _ io.ReadWriteCloser = (*File)(nil)
