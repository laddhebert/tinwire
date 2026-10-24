package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/laddhebert/tinwire/imsg"
)

func TestContactNamesFromAddressBook(t *testing.T) {
	m := fixture()
	m.contacts = map[string]string{"5550003333": "Carol Danvers", "bob@example.com": "Bob Builder"}
	m.chats = append(m.chats, imsg.Chat{ID: 4, Identifier: "+1 (555) 000-3333", Participants: []string{"+15550003333"}})
	if got := m.titleFor(m.chats[3]); got != "Carol Danvers" {
		t.Fatalf("10-digit match failed: %q", got)
	}
	// Group title uses first names; alias file still wins over AddressBook.
	if got := m.titleFor(m.chats[1]); got != "A very long group chat name that will not fit in the list column" {
		t.Fatalf("display name should win for groups: %q", got)
	}
	g := imsg.Chat{ID: 5, IsGroup: true, Participants: []string{"+15550001", "BOB@example.com"}}
	if got := m.titleFor(g); got != "Alice, Bob" {
		t.Fatalf("group title = %q", got)
	}
	// Typed name resolves to the handle for ctrl+n.
	if h, ok := m.resolveName("carol danvers"); !ok || h != "+15550003333" {
		t.Fatalf("resolveName = %q %v", h, ok)
	}
	if i := m.findHandle("5550003333"); i != 3 {
		t.Fatalf("findHandle by bare digits = %d", i)
	}
}

func TestCursorOlderPageAndTapbackTarget(t *testing.T) {
	m := resize(fixture(), 100, 30)
	m.open = 1
	m.api = &imsg.API{}
	m.focus = focusConv
	m.cursor = 0

	// Older page prepends, dedupes, and keeps the cursor on the same message.
	nm, _ := m.Update(olderMsg{chatID: 1, msgs: []imsg.Message{
		{ID: 5, ChatID: 1, Text: "older", CreatedAt: ts("2026-10-20T09:00:00Z")},
		{ID: 10, ChatID: 1, Text: "dup", CreatedAt: ts("2026-10-23T09:00:00Z")},
	}})
	m = nm.(Model)
	if n := len(m.msgs[1]); n != 4 || m.msgs[1][0].ID != 5 {
		t.Fatalf("older merge wrong: %d, first id %d", n, m.msgs[1][0].ID)
	}
	if m.msgs[1][m.cursor].ID != 10 {
		t.Fatalf("cursor moved off its message: now on %d", m.msgs[1][m.cursor].ID)
	}
	// An empty older page marks the conversation exhausted.
	nm, _ = m.Update(olderMsg{chatID: 1})
	m = nm.(Model)
	if !m.exhausted[1] {
		t.Fatal("not marked exhausted")
	}

	// Latest non-reaction message is ID 11 (12 is a reaction).
	for i, x := range m.msgs[1] {
		if got, want := m.isLatest(i), x.ID == 11; got != want {
			t.Errorf("isLatest(%d id=%d) = %v", i, x.ID, got)
		}
	}
	m.cursor = 0
	nm, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if mm := nm.(Model); mm.focus == focusReact || !strings.Contains(mm.status, "latest message") {
		t.Fatalf("react on an old message should be refused: focus=%v status=%q", mm.focus, mm.status)
	}
}

func TestCursorRenderAndScroll(t *testing.T) {
	m := resize(fixture(), 80, 12)
	m.open = 1
	for i := 0; i < 40; i++ {
		m.msgs[1] = append(m.msgs[1], imsg.Message{ID: int64(100 + i), ChatID: 1, Text: "filler line", CreatedAt: ts("2026-10-23T10:00:00Z")})
	}
	cmd := m.focusConversation()
	_ = cmd
	if m.cursor != len(m.msgs[1])-1 {
		t.Fatalf("cursor should start on the newest message, at %d", m.cursor)
	}
	m.cursor = 0
	m.refreshVP(false)
	if m.vp.YOffset > m.lineStarts[0] {
		t.Fatalf("cursor at top not scrolled into view: yoff=%d start=%d", m.vp.YOffset, m.lineStarts[0])
	}
	if out, _ := m.renderConversation(70); !strings.Contains(out, "▌") {
		t.Fatal("cursor bar not rendered")
	}
}

func TestTypingIndicatorLifecycle(t *testing.T) {
	m := resize(fixture(), 100, 30)
	m.api = &imsg.API{}
	m.connected = true
	m.typingOK = true
	m.open = 1
	m.focus = focusInput
	m.ta.Focus()

	nm, cmd := m.keyInput(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")})
	m = nm.(Model)
	if !m.typingOn || m.typingFor != 1 || cmd == nil {
		t.Fatalf("first keystroke should start typing: on=%v for=%d", m.typingOn, m.typingFor)
	}
	// A refused indicator turns the feature off for the session.
	nm, _ = m.Update(typingErrMsg{err: os.ErrPermission})
	m = nm.(Model)
	if m.typingOK || m.typingOn {
		t.Fatal("typing should be disabled after an error")
	}
	nm, _ = m.keyInput(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	if nm.(Model).typingOn {
		t.Fatal("typing restarted after being disabled")
	}
}

func TestAttachPromptAndDryRunSend(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "pic.jpg")
	if err := os.WriteFile(f, []byte("jpeg"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := resize(fixture(), 100, 30)
	m.open = 1
	m.cfg.DryRun = true
	m.focus = focusAttach
	m.at.SetValue(dir) // a directory is refused
	nm, _ := m.keyAttach(tea.KeyMsg{Type: tea.KeyEnter})
	if mm := nm.(Model); len(mm.attach) != 0 || !strings.Contains(mm.status, "regular file") {
		t.Fatalf("directory accepted: %q", mm.status)
	}
	m.at.SetValue(f)
	nm, _ = m.keyAttach(tea.KeyMsg{Type: tea.KeyEnter})
	m = nm.(Model)
	if len(m.attach) != 1 || m.attach[0] != f || m.focus != focusInput {
		t.Fatalf("attach not set: %q focus=%v", m.attach, m.focus)
	}
	if !strings.Contains(m.renderComposeRule(80), "pic.jpg") {
		t.Fatal("attachment not shown above compose box")
	}
	if cmd := m.send(); cmd != nil || !strings.Contains(m.status, "pic.jpg") || len(m.attach) != 0 {
		t.Fatalf("dry-run attachment send: %q attach=%q", m.status, m.attach)
	}
}

func TestOnlySafeTypesAutoOpen(t *testing.T) {
	for _, ext := range []string{".desktop", ".sh", ".exe", ".html", ".svg", ".appimage", ".jar"} {
		if openable[ext] {
			t.Errorf("%s must not be auto-opened", ext)
		}
	}
	for _, ext := range []string{".jpg", ".png", ".mp4", ".m4a", ".pdf"} {
		if !openable[ext] {
			t.Errorf("%s should open", ext)
		}
	}
}

func TestReactionAndReplyContext(t *testing.T) {
	m := fixture()
	m.open = 1
	m.msgs[1][0].GUID = "G-HELLO"
	m.msgs[1][2].ReactedToGUID = "p:0/G-HELLO"
	m.msgs[1] = append(m.msgs[1], imsg.Message{ID: 13, ChatID: 1, Sender: "+15550001", Text: "answer", ReplyToGUID: "G-HELLO", CreatedAt: ts("2026-10-23T09:03:00Z")})
	out, _ := m.renderConversation(120)
	if !strings.Contains(out, "reacted ♥ to “hello there") || !strings.Contains(out, "↳ “hello there") {
		t.Fatalf("reaction/reply context missing:\n%s", out)
	}
}
