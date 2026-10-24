package keyfix

import (
	"io"
	"os"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTranslate(t *testing.T) {
	cases := map[string]string{
		"\x1b[13;2u":           "\x1b\r", // shift+enter (foot on Omarchy, kitty, ghostty)
		"\x1b[13;5u":           "\x1b\r", // ctrl+enter
		"\x1b[27;2;13~":        "\x1b\r", // xterm modifyOtherKeys
		"ab\x1b[13;2ucd":       "ab\x1b\rcd",
		"\x1b[13;2u\x1b[13;2u": "\x1b\r\x1b\r",
		"\x1b[A":               "\x1b[A", // arrow keys untouched
		"\x1b":                 "\x1b",   // lone ESC untouched
		"\r":                   "\r",     // plain enter untouched
		"hello":                "hello",
	}
	for in, want := range cases {
		if got := string(Translate([]byte(in))); got != want {
			t.Errorf("Translate(%q) = %q, want %q", in, got, want)
		}
	}
}

type firstKey struct{ got chan string }

func (f firstKey) Init() tea.Cmd { return nil }
func (f firstKey) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		if k.String() == "q" {
			return f, tea.Quit
		}
		f.got <- k.String()
	}
	return f, nil
}
func (f firstKey) View() string { return "" }

// Real Bubble Tea reading through the wrapper, with fast typing right after
// the Shift+Enter (the case that broke decoding inside Update).
func TestBubbleTeaSeesAltEnter(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	f := firstKey{got: make(chan string, 16)}
	p := tea.NewProgram(f, tea.WithInput(Wrap(r)), tea.WithOutput(io.Discard))
	done := make(chan struct{})
	go func() { p.Run(); close(done) }()
	w.Write([]byte("\x1b[13;2u"))
	w.Write([]byte("zzzz"))
	time.Sleep(100 * time.Millisecond)
	w.Write([]byte("q"))
	select {
	case k := <-f.got:
		if k != "alt+enter" {
			t.Fatalf("first key = %q, want alt+enter", k)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no key")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		p.Kill()
	}
}
