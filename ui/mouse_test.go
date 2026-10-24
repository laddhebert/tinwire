package ui

import (
	"fmt"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/laddhebert/tinwire/imsg"
)

func click(m Model, x, y int) Model {
	nm, _ := m.Update(tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	return nm.(Model)
}

func wheel(m Model, x, y int, b tea.MouseButton) Model {
	nm, _ := m.Update(tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: b})
	return nm.(Model)
}

func TestClickChatOpensIt(t *testing.T) {
	m := resize(fixture(), 100, 30)
	m = click(m, 5, 2) // third row
	if m.open != 3 || m.sel != 2 || m.focus != focusInput {
		t.Fatalf("open=%d sel=%d focus=%v", m.open, m.sel, m.focus)
	}
	// Click below the last chat does nothing.
	before := m.open
	m = click(m, 5, 20)
	if m.open != before {
		t.Fatal("click on empty list area changed the chat")
	}
}

func TestClickMapsToRowWhenListIsScrolled(t *testing.T) {
	m := resize(fixture(), 100, 12) // list height 11
	m.chats = nil
	for i := 1; i <= 40; i++ {
		m.chats = append(m.chats, imsg.Chat{ID: int64(i), Identifier: fmt.Sprintf("+1555000%04d", i), Participants: []string{fmt.Sprintf("+1555000%04d", i)}})
	}
	// Scroll the list by moving the selection down with the wheel.
	for i := 0; i < 25; i++ {
		m = wheel(m, 3, 3, tea.MouseButtonWheelDown)
	}
	if m.sel != 25 || m.listTop == 0 {
		t.Fatalf("sel=%d listTop=%d", m.sel, m.listTop)
	}
	top := m.listTop
	m = click(m, 3, 0) // first visible row
	if m.open != m.chats[top].ID {
		t.Fatalf("clicked row 0 (chat %d) but opened %d", m.chats[top].ID, m.open)
	}
}

func TestClickMessageSelectsIt(t *testing.T) {
	m := resize(fixture(), 120, 30)
	m.open = 1
	m.focus = focusInput
	m.refreshVP(true)
	listW, _, _, _ := m.dims()
	// Content line 0 is the date separator, line 1 the first message.
	first := m.lineStarts[0]
	m = click(m, listW+5, 1+first-m.vp.YOffset)
	if m.focus != focusConv || m.cursor != 0 {
		t.Fatalf("focus=%v cursor=%d", m.focus, m.cursor)
	}
	second := m.lineStarts[1]
	m = click(m, listW+5, 1+second-m.vp.YOffset)
	if m.cursor != 1 {
		t.Fatalf("cursor=%d, want 1", m.cursor)
	}
}

func TestClickComposeFocusesIt(t *testing.T) {
	m := resize(fixture(), 120, 30)
	m.open = 1
	m.focus = focusList
	listW, _, bodyH, _ := m.dims()
	m = click(m, listW+5, bodyH-1)
	if m.focus != focusInput || !m.ta.Focused() {
		t.Fatalf("focus=%v focused=%v", m.focus, m.ta.Focused())
	}
}

func TestMouseIgnoredInPromptsAndStatusBar(t *testing.T) {
	m := resize(fixture(), 120, 30)
	m.focus = focusTo
	m = click(m, 5, 1)
	if m.open != 0 || m.focus != focusTo {
		t.Fatal("click acted during the To: prompt")
	}
	m.focus = focusList
	m = click(m, 5, 29) // status bar
	if m.open != 0 {
		t.Fatal("status bar click opened a chat")
	}
}
