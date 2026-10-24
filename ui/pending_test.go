package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/laddhebert/tinwire/imsg"
)

func conv(m Model) string { s, _ := m.renderConversation(120); return stripANSI(s) }

func TestSendShowsPlaceholderThenDeliveryState(t *testing.T) {
	m := resize(fixture(), 140, 40)
	m.open = 1
	m.api = &imsg.API{} // the returned command is not run here
	m.connected = true
	m.focus = focusInput
	m.ta.Focus()
	m.ta.SetValue("on my way")
	if cmd := m.send(); cmd == nil {
		t.Fatal("send returned no command")
	}
	if c := conv(m); !strings.Contains(c, "⏳ sending…") || !strings.Contains(c, "on my way") {
		t.Fatalf("no placeholder right after enter:\n%s", c)
	}

	// Messages records the message: the watch delivers it, it replaces the placeholder.
	m.applyIncoming(imsg.Message{ID: 50, ChatID: 1, GUID: "G-SENT", IsFromMe: true, Text: "on my way", CreatedAt: ts("2026-10-24T05:30:00Z")})
	c := conv(m)
	if strings.Contains(c, "⏳ sending…") {
		t.Fatalf("placeholder not replaced:\n%s", c)
	}
	if !strings.Contains(c, "on my way  ✓ sent") {
		t.Fatalf("no sent mark:\n%s", c)
	}

	// Apple confirms delivery.
	m.applyDelivery(deliveryMsg{states: map[string]string{"G-SENT": "delivered"}})
	if c := conv(m); !strings.Contains(c, "✓✓ delivered") {
		t.Fatalf("no delivered mark:\n%s", c)
	}
}

func TestFailedSendStaysVisible(t *testing.T) {
	m := resize(fixture(), 140, 40)
	m.open = 1
	m.api = &imsg.API{}
	m.connected = true
	m.ta.SetValue("hello?")
	m.send()
	pid := m.pending[0].id
	nm, _ := m.Update(sentMsg{chatID: 1, text: "hello?", pid: pid, err: errors.New("Messages is not signed in")})
	m = nm.(Model)
	if c := conv(m); !strings.Contains(c, "✗ not sent: Messages is not signed in") {
		t.Fatalf("failure not shown in the conversation:\n%s", c)
	}
	// The next send in that chat clears the old failure.
	m.sending = false
	m.ta.SetValue("again")
	m.send()
	if c := conv(m); strings.Contains(c, "✗ not sent") || !strings.Contains(c, "again") {
		t.Fatalf("old failure not cleared:\n%s", c)
	}
}

func TestPlaceholderForNewConversation(t *testing.T) {
	m := resize(fixture(), 140, 40)
	m.open, m.openTo = 0, "+15550199"
	m.api = &imsg.API{}
	m.connected = true
	m.ta.SetValue("hi")
	m.send()
	if c := conv(m); !strings.Contains(c, "⏳ sending…") {
		t.Fatalf("no placeholder in a new conversation:\n%s", c)
	}
}
