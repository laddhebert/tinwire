package ui

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func touch(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDroppedFilesFormats(t *testing.T) {
	dir := t.TempDir()
	a := touch(t, dir, "my photo.jpg")
	b := touch(t, dir, "b.png")
	escaped := strings.ReplaceAll(a, " ", `\ `)

	cases := []struct {
		name string
		in   string
		want []string
		ok   bool
	}{
		{"foot uri-list", "file://" + strings.ReplaceAll(a, " ", "%20") + "\r\nfile://" + b + "\r\n", []string{a, b}, true},
		{"uri with host", "file://localhost" + b, []string{b}, true},
		{"raw path with spaces (alacritty)", a, []string{a}, true},
		{"shell-escaped (ghostty)", escaped + " " + b, []string{a, b}, true},
		{"quoted", `'` + a + `' "` + b + `"`, []string{a, b}, true},
		{"trailing newline", b + "\n", []string{b}, true},
		{"ordinary text", "see you at 5", nil, false},
		{"text mentioning a real path", "look at " + b, nil, false},
		{"missing file", filepath.Join(dir, "nope.jpg"), nil, false},
		{"directory", dir, nil, false},
		{"uri to missing file", "file://" + filepath.Join(dir, "gone.png"), nil, false},
		{"empty", "   ", nil, false},
	}
	for _, c := range cases {
		got, ok := droppedFiles(c.in)
		if ok != c.ok || (c.ok && !reflect.DeepEqual(got, c.want)) {
			t.Errorf("%s: got %v %v, want %v %v", c.name, got, ok, c.want, c.ok)
		}
	}
}

func pasteKey(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s), Paste: true}
}

func TestDropOntoListAttachesAndFocusesCompose(t *testing.T) {
	dir := t.TempDir()
	f := touch(t, dir, "pic.jpg")
	m := resize(fixture(), 100, 30)
	m.open = 1
	m.focus = focusList
	nm, _ := m.Update(pasteKey("file://" + f))
	m = nm.(Model)
	if len(m.attach) != 1 || m.attach[0] != f || m.focus != focusInput {
		t.Fatalf("attach=%v focus=%v", m.attach, m.focus)
	}
	// Dropping the same file again does not duplicate it; a second file adds.
	g := touch(t, dir, "two.png")
	nm, _ = m.Update(pasteKey(f + " " + g))
	m = nm.(Model)
	if len(m.attach) != 2 {
		t.Fatalf("attach=%v", m.attach)
	}
	if !strings.Contains(m.renderComposeRule(80), "pic.jpg, two.png") {
		t.Fatalf("compose rule: %q", m.renderComposeRule(80))
	}
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlX})
	if len(nm.(Model).attach) != 0 {
		t.Fatal("ctrl+x did not clear attachments")
	}
}

func TestPlainPasteStillTypes(t *testing.T) {
	m := resize(fixture(), 100, 30)
	m.open = 1
	m.focus = focusInput
	m.ta.Focus()
	nm, _ := m.Update(pasteKey("hello there"))
	m = nm.(Model)
	if m.ta.Value() != "hello there" || len(m.attach) != 0 {
		t.Fatalf("value=%q attach=%v", m.ta.Value(), m.attach)
	}
}

func TestDropNeedsOpenChatAndRespectsLimit(t *testing.T) {
	dir := t.TempDir()
	f := touch(t, dir, "pic.jpg")
	m := resize(fixture(), 100, 30)
	nm, _ := m.Update(pasteKey(f))
	if mm := nm.(Model); len(mm.attach) != 0 || !strings.Contains(mm.status, "open a chat first") {
		t.Fatalf("attached without a chat: %q", mm.status)
	}
	m.open = 1
	m.cfg.MaxAttach = -1
	nm, _ = m.Update(pasteKey(f))
	if mm := nm.(Model); len(mm.attach) != 0 || !strings.Contains(mm.status, "limit") {
		t.Fatalf("size limit not enforced: %q", mm.status)
	}
}

func TestAttachPromptAcceptsDropAndMultiDryRun(t *testing.T) {
	dir := t.TempDir()
	a, b := touch(t, dir, "a.jpg"), touch(t, dir, "b.jpg")
	m := resize(fixture(), 100, 30)
	m.open = 1
	m.cfg.DryRun = true
	m.focus = focusAttach
	nm, _ := m.Update(pasteKey("file://" + a + "\nfile://" + b))
	m = nm.(Model)
	if len(m.attach) != 2 || m.focus != focusInput {
		t.Fatalf("attach=%v focus=%v", m.attach, m.focus)
	}
	if cmd := m.send(); cmd != nil || !strings.Contains(m.status, "2 files") {
		t.Fatalf("dry-run multi send: %q", m.status)
	}
}

func TestUnbracketedDropBurstAttaches(t *testing.T) {
	dir := t.TempDir()
	f := touch(t, dir, "clip.mp4")
	m := resize(fixture(), 100, 30)
	m.open = 1
	m.focus = focusInput
	m.ta.Focus()
	// No bracketed-paste markers: the whole path arrives as one burst of runes.
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("file://" + f)})
	m = nm.(Model)
	if len(m.attach) != 1 || m.ta.Value() != "" {
		t.Fatalf("unbracketed drop: attach=%v compose=%q", m.attach, m.ta.Value())
	}
	// Ordinary multi-rune text (e.g. IME input) still types normally.
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("héllo")})
	if nm.(Model).ta.Value() != "héllo" {
		t.Fatalf("text burst not typed: %q", nm.(Model).ta.Value())
	}
}

func TestDropSendsImmediately(t *testing.T) {
	f := touch(t, t.TempDir(), "photo.jpg")
	m := resize(fixture(), 140, 40)
	m.open = 1
	m.cfg.SendOnDrop = true
	m.cfg.DryRun = true
	m.focus = focusInput
	m.ta.Focus()
	m.ta.SetValue("look at this")
	nm, _ := m.Update(pasteKey("file://" + f))
	m = nm.(Model)
	if !strings.Contains(m.status, "would send 12 chars + photo.jpg") || len(m.attach) != 0 || m.ta.Value() != "" {
		t.Fatalf("drop did not send: status=%q attach=%v compose=%q", m.status, m.attach, m.ta.Value())
	}
	// From the chat list too: the drop opens nothing new, it sends to the open chat.
	m.focus = focusList
	nm, _ = m.Update(pasteKey(f))
	if !strings.Contains(nm.(Model).status, "would send 0 chars + photo.jpg") {
		t.Fatalf("drop from list focus: %q", nm.(Model).status)
	}
}

func TestDropWithoutAutoSendOnlyAttaches(t *testing.T) {
	f := touch(t, t.TempDir(), "photo.jpg")
	m := resize(fixture(), 140, 40)
	m.open = 1
	m.cfg.SendOnDrop = false
	m.cfg.DryRun = true
	nm, _ := m.Update(pasteKey(f))
	if mm := nm.(Model); len(mm.attach) != 1 || strings.Contains(mm.status, "would send") {
		t.Fatalf("--no-send-on-drop should only attach: %v %q", mm.attach, mm.status)
	}
}
