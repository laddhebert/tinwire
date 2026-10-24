package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/laddhebert/tinwire/imsg"
)

func TestClickAttachmentLineOpensIt(t *testing.T) {
	m := resize(fixture(), 140, 40)
	m.open = 1
	m.refreshVP(true)
	// Message 1 (ID 11) carries pic.heic.
	line := -1
	for l, ref := range m.attachLines {
		if ref.msg == 1 && ref.att == 0 {
			line = l
		}
	}
	if line < 0 {
		t.Fatalf("attachment line not found: %v", m.attachLines)
	}
	if !strings.Contains(stripANSI(m.vpLines[line]), "click to open") {
		t.Fatalf("no hint on attachment line: %q", stripANSI(m.vpLines[line]))
	}
	listW, _, _, _ := m.dims()
	row := 1 + line - m.vp.YOffset
	m, _ = mouseAt(m, listW+20, row, tea.MouseActionPress)
	m, cmd := mouseAt(m, listW+20, row, tea.MouseActionRelease)
	if cmd == nil || !strings.Contains(m.status, "fetching attachment") {
		t.Fatalf("click on attachment did not start opening: status=%q", m.status)
	}
	// A click on a text line does not open anything.
	text := m.lineStarts[0]
	m, _ = mouseAt(m, listW+20, 1+text-m.vp.YOffset, tea.MouseActionPress)
	m.status = ""
	_, cmd = mouseAt(m, listW+20, 1+text-m.vp.YOffset, tea.MouseActionRelease)
	if cmd != nil {
		t.Fatal("click on a text line opened something")
	}
}

func TestQuotedClipIsNotAnAttachmentLine(t *testing.T) {
	m := resize(fixture(), 140, 40)
	m.open = 1
	m.msgs[1][1].GUID = "G-PIC"
	m.msgs[1] = append(m.msgs[1], imsg.Message{ID: 13, ChatID: 1, Sender: "+15550001", Text: "nice", ReplyToGUID: "G-PIC", CreatedAt: ts("2026-10-23T09:05:00Z")})
	m.refreshVP(true)
	for _, ref := range m.attachLines {
		if ref.msg == 3 {
			t.Fatal("the quoted 📎 in a reply was treated as an attachment")
		}
	}
	if len(m.attachLines) != 1 {
		t.Fatalf("want exactly one attachment line, got %v", m.attachLines)
	}
}

func TestEnterOpensSelectedAttachment(t *testing.T) {
	m := resize(fixture(), 140, 40)
	m.open = 1
	m.focus = focusConv
	m.cursor = 1 // the message with pic.heic
	m.refreshVP(false)
	nm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = nm.(Model)
	if cmd == nil || !strings.Contains(m.status, "fetching attachment") || m.focus != focusConv {
		t.Fatalf("enter on attachment: status=%q focus=%v", m.status, m.focus)
	}
	m.cursor = 0 // text only: enter goes to compose
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if nm.(Model).focus != focusInput {
		t.Fatal("enter on a text message should go to compose")
	}
}

func TestSeenStatePersistsAndClearsBold(t *testing.T) {
	dir := t.TempDir()
	m := resize(fixture(), 100, 30)
	m.seen = loadSeen(dir)
	m.chats[2].GUID = "any;-;carol@example.com"
	m.chats[2].UnreadCount = 3

	m.openChat(m.chats[2].ID) // viewing it marks it seen
	if _, err := os.Stat(filepath.Join(dir, "seen.json")); err != nil {
		t.Fatalf("seen.json not written: %v", err)
	}
	if st, _ := os.Stat(filepath.Join(dir, "seen.json")); st.Mode().Perm() != 0o600 {
		t.Fatalf("seen.json mode %v", st.Mode().Perm())
	}

	// Restart: the Mac still reports 3 unread, but tinwire remembers.
	m2 := resize(fixture(), 100, 30)
	m2.seen = loadSeen(dir)
	fresh := append([]imsg.Chat(nil), m2.chats...)
	fresh[2].GUID = "any;-;carol@example.com"
	fresh[2].UnreadCount = 3
	m2.mergeChats(fresh)
	if m2.chats[2].UnreadCount != 0 {
		t.Fatal("viewed chat came back unread after restart")
	}

	// A newer message makes it unread again.
	fresh[2].LastMessageAt = ts("2026-10-24T09:00:00Z")
	fresh[2].UnreadCount = 3 // mergeChats adjusts the slice it is given
	m2.mergeChats(fresh)
	if m2.chats[2].UnreadCount != 3 {
		t.Fatal("new message after viewing should show as unread")
	}
}
