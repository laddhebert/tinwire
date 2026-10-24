package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func mouseAt(m Model, x, y int, a tea.MouseAction) (Model, tea.Cmd) {
	nm, cmd := m.Update(tea.MouseMsg{X: x, Y: y, Action: a, Button: tea.MouseButtonLeft})
	return nm.(Model), cmd
}

// screenRow returns the screen row of content line l in the conversation.
func screenRow(m Model, l int) int { return 1 + l - m.vp.YOffset }

func TestDragSelectsAndCopies(t *testing.T) {
	m := resize(fixture(), 140, 40)
	m.open = 1
	m.focus = focusInput
	m.refreshVP(true)
	listW, _, _, _ := m.dims()
	x0 := listW + 1
	row := screenRow(m, m.lineStarts[0]) // first message: "… hello there, this is a fairly long …"
	line := m.vpLines[m.lineStarts[0]]
	plain := stripANSI(line)
	start := strings.Index(plain, "hello")
	if start < 0 {
		t.Fatalf("fixture line has no 'hello': %q", plain)
	}
	startCol := lipgloss.Width(plain[:start])

	m, _ = mouseAt(m, x0+startCol, row, tea.MouseActionPress)
	if m.focus != focusConv || m.hasSel {
		t.Fatalf("press: focus=%v hasSel=%v", m.focus, m.hasSel)
	}
	m, _ = mouseAt(m, x0+startCol+10, row, tea.MouseActionMotion) // "hello there"
	if !m.hasSel {
		t.Fatal("drag did not start a selection")
	}
	if got := m.selectedText(); got != "hello there" {
		t.Fatalf("selectedText = %q", got)
	}
	m, cmd := mouseAt(m, x0+startCol+10, row, tea.MouseActionRelease)
	if cmd == nil {
		t.Fatal("release did not copy")
	}
	if !m.hasSel {
		t.Fatal("highlight should stay visible after copying")
	}
	// Any key clears it.
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if nm.(Model).hasSel {
		t.Fatal("key did not clear the selection")
	}
}

func TestPlainClickDoesNotSelect(t *testing.T) {
	m := resize(fixture(), 140, 40)
	m.open = 1
	m.refreshVP(true)
	listW, _, _, _ := m.dims()
	row := screenRow(m, m.lineStarts[1])
	m, _ = mouseAt(m, listW+10, row, tea.MouseActionPress)
	m, cmd := mouseAt(m, listW+10, row, tea.MouseActionRelease)
	if m.hasSel || cmd != nil || m.cursor != 1 {
		t.Fatalf("click: hasSel=%v cmd=%v cursor=%d", m.hasSel, cmd != nil, m.cursor)
	}
}

func TestMultiLineSelectionAndHighlight(t *testing.T) {
	lipgloss.SetColorProfile(termenv.ANSI)
	defer lipgloss.SetColorProfile(termenv.Ascii)
	m := resize(fixture(), 80, 30) // narrow: the first message wraps
	m.open = 1
	m.refreshVP(true)
	listW, _, _, _ := m.dims()
	x0 := listW + 1
	first := m.lineStarts[0]
	m, _ = mouseAt(m, x0+1, screenRow(m, first), tea.MouseActionPress)
	m, _ = mouseAt(m, x0+200, screenRow(m, first+1), tea.MouseActionMotion)
	got := m.selectedText()
	if strings.Count(got, "\n") != 1 || !strings.Contains(got, "hello there") {
		t.Fatalf("two-line selection = %q", got)
	}
	if strings.Contains(got, "▌") {
		t.Fatal("cursor bar leaked into the copied text")
	}
	if !strings.Contains(m.viewportView(), "\x1b[7m") {
		t.Fatal("selection not highlighted (reverse video)")
	}
}

func stripANSI(s string) string {
	var b strings.Builder
	esc := false
	for _, r := range s {
		switch {
		case r == '\x1b':
			esc = true
		case esc && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'):
			esc = false
		case !esc:
			b.WriteRune(r)
		}
	}
	return b.String()
}
