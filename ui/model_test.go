package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/laddhebert/tinwire/imsg"
)

func ts(s string) imsg.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return imsg.Time{Time: t}
}

func fixture() Model {
	m := New(Config{Host: "test", ChatLimit: 10, HistoryLimit: 10, MaxSends: 2,
		Aliases: map[string]string{"+15550001": "Alice"}})
	m.connected = true
	m.chats = []imsg.Chat{
		{ID: 1, Identifier: "+15550001", Participants: []string{"+15550001"}, Service: "iMessage", LastMessageAt: ts("2026-10-23T10:00:00Z")},
		{ID: 2, IsGroup: true, DisplayName: "A very long group chat name that will not fit in the list column", Participants: []string{"+15550001", "bob@example.com"}, LastMessageAt: ts("2026-10-22T10:00:00Z")},
		{ID: 3, Identifier: "carol@example.com", Participants: []string{"carol@example.com"}, LastMessageAt: ts("2026-09-01T10:00:00Z")},
	}
	m.msgs[1] = []imsg.Message{
		{ID: 10, ChatID: 1, Sender: "+15550001", Text: "hello there, this is a fairly long message that has to wrap across several lines in a narrow terminal", CreatedAt: ts("2026-10-23T09:00:00Z")},
		{ID: 11, ChatID: 1, IsFromMe: true, Text: "hi 👋", CreatedAt: ts("2026-10-23T09:01:00Z"), Attachments: []imsg.Attachment{{TransferName: "pic.heic", TotalBytes: 2 << 20}}},
		{ID: 12, ChatID: 1, Sender: "+15550001", IsReaction: true, IsReactionAdd: true, ReactionType: "love", CreatedAt: ts("2026-10-23T09:02:00Z")},
	}
	return m
}

func resize(m Model, w, h int) Model {
	nm, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return nm.(Model)
}

func TestViewFitsTerminal(t *testing.T) {
	for _, sz := range [][2]int{{50, 10}, {80, 24}, {120, 40}, {200, 60}} {
		m := resize(fixture(), sz[0], sz[1])
		m.open = 1
		m.refreshVP(true)
		out := m.View()
		lines := strings.Split(out, "\n")
		if len(lines) != sz[1] {
			t.Errorf("%dx%d: %d lines", sz[0], sz[1], len(lines))
		}
		for i, l := range lines {
			if w := lipgloss.Width(l); w > sz[0] {
				t.Errorf("%dx%d: line %d is %d wide", sz[0], sz[1], i, w)
			}
		}
	}
}

func TestTooSmall(t *testing.T) {
	if got := resize(fixture(), 30, 5).View(); got != "window too small" {
		t.Fatalf("got %q", got)
	}
}

func TestAliasesAndRendering(t *testing.T) {
	m := resize(fixture(), 120, 30)
	m.open = 1
	m.refreshVP(true)
	conv, _ := m.renderConversation(80)
	for _, want := range []string{"Alice", "me", "📎 pic.heic (2.0 MB)", "reacted ♥"} {
		if !strings.Contains(conv, want) {
			t.Errorf("conversation missing %q", want)
		}
	}
	if got := m.titleFor(m.chats[0]); got != "Alice" {
		t.Errorf("title = %q", got)
	}
}

func TestIncomingMovesChatAndCountsUnread(t *testing.T) {
	m := resize(fixture(), 100, 30)
	m.open = 1
	m.sel = 0
	m.applyIncoming(imsg.Message{ID: 99, ChatID: 3, Sender: "carol@example.com", Text: "yo", CreatedAt: ts("2026-10-23T11:00:00Z")})
	if m.chats[0].ID != 3 || m.chats[0].UnreadCount != 1 {
		t.Fatalf("chat 3 should be first with 1 unread: %+v", m.chats[0])
	}
	if m.chats[m.sel].ID != 1 {
		t.Fatalf("selection should stay on chat 1, is on %d", m.chats[m.sel].ID)
	}
	// Message into the open chat: appended, no unread.
	m.applyIncoming(imsg.Message{ID: 13, ChatID: 1, Sender: "+15550001", Text: "again", CreatedAt: ts("2026-10-23T11:01:00Z")})
	if n := len(m.msgs[1]); n != 4 {
		t.Fatalf("want 4 messages in open chat, got %d", n)
	}
	// Duplicate delivery of the same row is ignored.
	m.applyIncoming(imsg.Message{ID: 13, ChatID: 1, Text: "again", CreatedAt: ts("2026-10-23T11:01:00Z")})
	if n := len(m.msgs[1]); n != 4 {
		t.Fatalf("duplicate appended: %d", n)
	}
	// Unsend replaces in place.
	m.applyIncoming(imsg.Message{ID: 13, ChatID: 1, Type: "unsent", UnsentAt: ts("2026-10-23T11:02:00Z")})
	if conv, _ := m.renderConversation(80); !strings.Contains(conv, "(unsent)") {
		t.Fatal("unsent not rendered")
	}
}

func TestSendGuards(t *testing.T) {
	m := resize(fixture(), 100, 30)
	m.open = 1

	m.cfg.DryRun = true
	m.ta.SetValue("hello")
	if cmd := m.send(); cmd != nil || m.sends != 0 || !strings.Contains(m.status, "dry-run") {
		t.Fatalf("dry-run should not send: sends=%d status=%q", m.sends, m.status)
	}

	m.cfg.DryRun = false
	m.api = &imsg.API{} // never called: every case below must stop before sending
	m.sends = m.cfg.MaxSends
	m.ta.SetValue("hello")
	if cmd := m.send(); cmd != nil || !strings.Contains(m.status, "send cap") {
		t.Fatalf("cap not enforced: %q", m.status)
	}

	m.sends = 0
	m.connected = false
	m.ta.SetValue("hello")
	if cmd := m.send(); cmd != nil || !strings.Contains(m.status, "not connected") {
		t.Fatalf("disconnected send allowed: %q", m.status)
	}
}

func TestUnreadChatsAreBold(t *testing.T) {
	lipgloss.SetColorProfile(termenv.ANSI) // tests have no TTY; force styling on
	defer lipgloss.SetColorProfile(termenv.Ascii)
	m := resize(fixture(), 100, 30)
	m.focus = focusInput
	m.sel = 0
	m.chats[2].UnreadCount = 2 // carol: unread, not selected
	m.chats[0].UnreadCount = 1 // Alice: unread and selected
	rows := strings.Split(m.renderList(36, 10), "\n")
	bold := "\x1b[1"
	if !strings.Contains(rows[2], bold) {
		t.Errorf("unread row not bold: %q", rows[2])
	}
	if !strings.Contains(rows[0], bold) {
		t.Errorf("selected unread row not bold: %q", rows[0])
	}
	if strings.Contains(rows[1], bold) {
		t.Errorf("read row is bold: %q", rows[1])
	}
	// Opening the chat clears unread, so it stops being bold.
	m.openChat(m.chats[2].ID)
	rows = strings.Split(m.renderList(36, 10), "\n")
	if strings.Contains(rows[2], bold) {
		t.Errorf("opened chat still bold: %q", rows[2])
	}
}

func TestStatusBarKeepsHintsWithLongStatus(t *testing.T) {
	for _, w := range []int{80, 140} {
		m := resize(fixture(), w, 30)
		m.focus = focusInput
		m.setStatus("connected to alice@mac.local · imsg 0.19.1 · and a very long tail that would not fit", false)
		bar := m.renderStatus()
		if lipgloss.Width(bar) > w {
			t.Errorf("w=%d: status bar is %d wide", w, lipgloss.Width(bar))
		}
		if !strings.Contains(bar, "↵ send") {
			t.Errorf("w=%d: hints dropped: %q", w, bar)
		}
	}
}

func TestAltEnterIsNewline(t *testing.T) {
	m := resize(fixture(), 100, 30)
	m.open = 1
	m.focus = focusInput
	m.ta.Focus()
	m.ta.SetValue("line one")
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter, Alt: true}) // what keyfix turns Shift+Enter into
	m = nm.(Model)
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("line two")})
	m = nm.(Model)
	if got := m.ta.Value(); got != "line one\nline two" || m.sending {
		t.Fatalf("compose = %q sending=%v", got, m.sending)
	}
}
