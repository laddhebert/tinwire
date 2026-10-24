package ui

import (
	"fmt"
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/laddhebert/tinwire/imsg"
)

// TestDemoScreen prints the UI with made-up data, for the README screenshot.
// Run: TINWIRE_DEMO=1 go test -run TestDemoScreen ./ui
func TestDemoScreen(t *testing.T) {
	if os.Getenv("TINWIRE_DEMO") == "" {
		t.Skip("set TINWIRE_DEMO=1 to print the demo screen")
	}
	m := New(Config{Host: "you@your-mac", ChatLimit: 10, HistoryLimit: 10,
		Aliases: map[string]string{"+15550100": "Sam Rivera", "+15550101": "Jordan Lee", "jamie@example.com": "Jamie Park"}})
	m.connected = true
	m.chats = []imsg.Chat{
		{ID: 1, Identifier: "+15550100", Participants: []string{"+15550100"}, Service: "iMessage", LastMessageAt: ts("2026-10-24T18:42:00Z")},
		{ID: 2, IsGroup: true, DisplayName: "Climbing crew", Participants: []string{"+15550101", "jamie@example.com"}, LastMessageAt: ts("2026-10-24T17:05:00Z"), UnreadCount: 3},
		{ID: 3, Identifier: "+15550101", Participants: []string{"+15550101"}, Service: "iMessage", LastMessageAt: ts("2026-10-23T21:10:00Z")},
		{ID: 4, Identifier: "jamie@example.com", Participants: []string{"jamie@example.com"}, Service: "iMessage", LastMessageAt: ts("2026-10-21T09:30:00Z"), UnreadCount: 1},
	}
	m.msgs[1] = []imsg.Message{
		{ID: 1, ChatID: 1, Sender: "+15550100", GUID: "A", Text: "are you still up for tacos tonight?", CreatedAt: ts("2026-10-24T18:30:00Z")},
		{ID: 2, ChatID: 1, IsFromMe: true, Text: "yes! 7:30 at the usual place", CreatedAt: ts("2026-10-24T18:31:00Z")},
		{ID: 3, ChatID: 1, Sender: "+15550100", Text: "perfect, here's the new menu", CreatedAt: ts("2026-10-24T18:40:00Z"),
			Attachments: []imsg.Attachment{{TransferName: "IMG_4021.HEIC", TotalBytes: 2310000}}},
		{ID: 4, ChatID: 1, Sender: "+15550100", IsReaction: true, IsReactionAdd: true, ReactionType: "love", ReactedToGUID: "A", CreatedAt: ts("2026-10-24T18:41:00Z")},
		{ID: 5, ChatID: 1, IsFromMe: true, Text: "sent from my Linux box, by the way", CreatedAt: ts("2026-10-24T18:42:00Z")},
	}
	m.open, m.sel, m.focus = 1, 0, focusInput
	nm, _ := m.Update(windowSize(100, 22))
	m = nm.(Model)
	m.ta.Focus()
	m.ta.SetValue("on my way")
	m.setStatus("connected to you@your-mac · imsg 0.19.1", false)
	fmt.Println(strings.TrimRight(stripANSI(m.View()), "\n"))
}

func windowSize(w, h int) tea.Msg { return tea.WindowSizeMsg{Width: w, Height: h} }
