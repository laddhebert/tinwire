package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Text selection inside the conversation pane. Mouse capture (needed for
// clicks and the wheel) stops the terminal from selecting text itself, so the
// app does it: press and drag over the conversation to highlight, release to
// copy the plain text to the clipboard. Positions are in conversation content
// coordinates (line in the full content, cell column), so a selection
// survives scrolling.

type textPos struct{ line, col int }

func (a textPos) before(b textPos) bool {
	return a.line < b.line || (a.line == b.line && a.col < b.col)
}

var selHighlight = lipgloss.NewStyle().Reverse(true)

// selRange returns the selection normalized so start <= end.
func (m Model) selRange() (textPos, textPos) {
	if m.selB.before(m.selA) {
		return m.selB, m.selA
	}
	return m.selA, m.selB
}

// contentPos converts a screen position in the conversation pane into a
// content position, clamped to the content.
func (m Model) contentPos(x, y, convX0, vpTop int) textPos {
	line := m.vp.YOffset + y - vpTop
	if line < 0 {
		line = 0
	}
	if n := len(m.vpLines); n > 0 && line >= n {
		line = n - 1
	}
	col := x - convX0
	if col < 0 {
		col = 0
	}
	return textPos{line, col}
}

// lineSpan returns the cell range [from, to) of content line l that the
// selection covers. The first cell (the message cursor bar) is never part of
// a selection.
func (m Model) lineSpan(l, width int) (from, to int, ok bool) {
	s, e := m.selRange()
	if l < s.line || l > e.line {
		return 0, 0, false
	}
	from, to = 0, width
	if l == s.line {
		from = s.col
	}
	if l == e.line {
		to = e.col + 1 // the end cell is inclusive
	}
	from = max(from, 1)
	to = min(to, width)
	return from, to, from < to
}

// selectedText is the plain text under the selection, one line per row,
// trailing blanks trimmed.
func (m Model) selectedText() string {
	if !m.hasSel {
		return ""
	}
	s, e := m.selRange()
	var out []string
	for l := s.line; l <= e.line && l < len(m.vpLines); l++ {
		plain := ansi.Strip(m.vpLines[l])
		from, to, ok := m.lineSpan(l, ansi.StringWidth(plain))
		if !ok {
			out = append(out, "")
			continue
		}
		out = append(out, strings.TrimRight(ansi.Cut(plain, from, to), " "))
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}

// viewportView renders the conversation viewport with the selection
// highlighted. Highlighted lines lose their colours while selected.
func (m Model) viewportView() string {
	v := m.vp.View()
	if !m.hasSel {
		return v
	}
	lines := strings.Split(v, "\n")
	for i, line := range lines {
		l := m.vp.YOffset + i
		plain := ansi.Strip(line)
		from, to, ok := m.lineSpan(l, ansi.StringWidth(plain))
		if !ok {
			continue
		}
		lines[i] = ansi.Cut(plain, 0, from) + selHighlight.Render(ansi.Cut(plain, from, to)) + ansi.Cut(plain, to, ansi.StringWidth(plain))
	}
	return strings.Join(lines, "\n")
}

func (m *Model) clearSelection() {
	m.selecting, m.dragged, m.hasSel = false, false, false
}
