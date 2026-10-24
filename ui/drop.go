package ui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Dropping files on a terminal window makes the terminal paste text: foot
// sends newline-separated file:// URIs, Alacritty raw paths, Ghostty
// shell-escaped paths. droppedFiles recognizes those forms and returns the
// local paths, or ok=false when the paste is ordinary text.
func droppedFiles(s string) (paths []string, ok bool) {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
	if s == "" {
		return nil, false
	}
	exists := func(p string) bool {
		st, err := os.Stat(expandHome(p))
		return err == nil && !st.IsDir()
	}

	// 1. One or more file:// URIs (text/uri-list; comment lines start with #).
	var uris []string
	allURIs := true
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		for _, f := range strings.Fields(line) {
			if !strings.HasPrefix(f, "file://") {
				allURIs = false
				break
			}
			u, err := url.Parse(f)
			if err != nil || u.Path == "" {
				return nil, false
			}
			uris = append(uris, u.Path)
		}
		if !allURIs {
			break
		}
	}
	if allURIs && len(uris) > 0 {
		for _, p := range uris {
			if !exists(p) {
				return nil, false
			}
		}
		return uris, true
	}

	// 2. A single raw path, possibly containing spaces.
	if !strings.Contains(s, "\n") && exists(s) {
		return []string{expandHome(s)}, true
	}

	// 3. Shell-style words (quotes, backslash escapes), one or more paths.
	words, err := shellWords(s)
	if err != nil || len(words) == 0 {
		return nil, false
	}
	for _, w := range words {
		if !exists(w) {
			return nil, false
		}
	}
	for i, w := range words {
		words[i] = expandHome(w)
	}
	return words, true
}

// shellWords splits s like a POSIX shell would for quoting purposes: single
// and double quotes and backslash escapes. No expansion is performed.
func shellWords(s string) ([]string, error) {
	var words []string
	var cur strings.Builder
	inWord := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\':
			if i+1 < len(s) {
				i++
				cur.WriteByte(s[i])
				inWord = true
			}
		case c == '\'':
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				return nil, errors.New("unterminated quote")
			}
			cur.WriteString(s[i+1 : i+1+j])
			i += j + 1
			inWord = true
		case c == '"':
			i++
			for ; i < len(s) && s[i] != '"'; i++ {
				if s[i] == '\\' && i+1 < len(s) && strings.IndexByte(`"\$`+"`", s[i+1]) >= 0 {
					i++
				}
				cur.WriteByte(s[i])
			}
			if i >= len(s) {
				return nil, errors.New("unterminated quote")
			}
			inWord = true
		case c == ' ' || c == '\t' || c == '\n':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words, nil
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p[1:], "/"))
		}
	}
	return p
}

// checkFiles validates paths for sending: regular files under the size limit.
func (m Model) checkFiles(paths []string) error {
	for _, p := range paths {
		st, err := os.Stat(p)
		switch {
		case err != nil:
			return err
		case !st.Mode().IsRegular():
			return fmt.Errorf("%s is not a regular file", filepath.Base(p))
		case st.Size() > m.cfg.MaxAttach:
			return fmt.Errorf("%s is %s, over the %s limit", filepath.Base(p), humanBytes(st.Size()), humanBytes(m.cfg.MaxAttach))
		}
	}
	return nil
}

// addAttachments queues files for the next send and moves focus to compose.
func (m *Model) addAttachments(paths []string) tea.Cmd {
	if m.open == 0 && m.openTo == "" {
		m.setStatus("open a chat first, then drop or paste the file", true)
		return nil
	}
	// Oversized videos are compressed to fit (video.go) instead of refused.
	var big, rest []string
	for _, p := range paths {
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && st.Size() > m.cfg.MaxAttach && isVideo(p) {
			big = append(big, p)
		} else {
			rest = append(rest, p)
		}
	}
	var cmds []tea.Cmd
	for _, p := range big {
		cmds = append(cmds, m.startCompress(p))
	}
	if len(rest) == 0 {
		if m.focus != focusInput {
			m.focus = focusInput
			m.refreshVP(true)
			cmds = append(cmds, m.ta.Focus())
		}
		return tea.Batch(cmds...)
	}
	paths = rest
	if err := m.checkFiles(paths); err != nil {
		m.setStatus("attach: "+err.Error(), true)
		return tea.Batch(cmds...)
	}
	seen := map[string]bool{}
	for _, p := range m.attach {
		seen[p] = true
	}
	for _, p := range paths {
		if !seen[p] {
			m.attach = append(m.attach, p)
			seen[p] = true
		}
	}
	m.layout()
	n := len(m.attach)
	what := filepath.Base(m.attach[n-1])
	if n > 1 {
		what = fmt.Sprintf("%d files", n)
	}
	if m.compress == nil {
		m.setStatus("attached "+what+" · enter sends · ctrl+x removes", false)
	}
	cmds = append(cmds, m.stopTyping())
	if m.focus != focusInput {
		m.focus = focusInput
		m.refreshVP(true)
		cmds = append(cmds, m.ta.Focus())
	}
	return tea.Batch(cmds...)
}

// handlePaste attaches dropped/pasted files; anything else is ordinary text
// for the compose box. It returns handled=false to let the caller deal with
// plain text in its own way.
//
// Some terminals deliver a drop without bracketed-paste markers, as a burst
// of plain input. Typing arrives one key at a time, so any multi-rune chunk is
// also checked: if it is entirely existing files it is a drop.
func (m *Model) handlePaste(k tea.KeyMsg) (tea.Cmd, bool) {
	burst := k.Type == tea.KeyRunes && len(k.Runes) > 1
	if !k.Paste && !burst {
		return nil, false
	}
	text := string(k.Runes)
	paths, ok := droppedFiles(text)
	logInputChunk(k.Paste, text, paths, ok)
	if !ok {
		return nil, false
	}
	return m.addAttachments(paths), true
}

// logInputChunk records the shape of a pasted/dropped chunk for --debug-log.
// Text that does not look like paths is never logged, only its size.
func logInputChunk(paste bool, text string, paths []string, ok bool) {
	t := strings.TrimSpace(text)
	looksLikePath := strings.HasPrefix(t, "file://") || strings.HasPrefix(t, "/") || strings.HasPrefix(t, "~") ||
		strings.HasPrefix(t, "'") || strings.HasPrefix(t, `"`)
	if !looksLikePath {
		debugf("input chunk: paste=%v runes=%d lines=%d looks-like-path=false", paste, len([]rune(text)), strings.Count(text, "\n")+1)
		return
	}
	if len(t) > 400 {
		t = t[:400] + "…"
	}
	debugf("input chunk: paste=%v runes=%d recognized-files=%v (%d) raw=%q", paste, len([]rune(text)), ok, len(paths), t)
	if !ok {
		for _, w := range strings.Fields(strings.NewReplacer("\r", " ", "\n", " ").Replace(text)) {
			p := w
			if strings.HasPrefix(p, "file://") {
				if u, err := url.Parse(p); err == nil {
					p = u.Path
				}
			}
			_, err := os.Stat(expandHome(p))
			debugf("  candidate %q exists=%v", p, err == nil)
		}
	}
}

type clipboardMsg struct {
	files []string
	text  string
	err   error
}

// pasteClipboard reads the Wayland clipboard: an image is saved to the cache
// and attached, copied files (text/uri-list) are attached, text is inserted.
func (m Model) pasteClipboard() tea.Cmd {
	dir := filepath.Join(filepath.Dir(m.cfg.CacheDir), "clipboard")
	return func() tea.Msg {
		if _, err := exec.LookPath("wl-paste"); err != nil {
			return clipboardMsg{err: errors.New("ctrl+v needs wl-paste (wl-clipboard); use your terminal's paste for text")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "wl-paste", "--list-types").Output()
		if err != nil {
			return clipboardMsg{err: errors.New("clipboard is empty")}
		}
		types := strings.Fields(string(out))
		pick := func(prefix string) string {
			for _, t := range types {
				if strings.HasPrefix(t, prefix) {
					return t
				}
			}
			return ""
		}
		if t := pick("text/uri-list"); t != "" {
			b, err := exec.CommandContext(ctx, "wl-paste", "--no-newline", "--type", t).Output()
			if err == nil {
				if paths, ok := droppedFiles(string(b)); ok {
					return clipboardMsg{files: paths}
				}
			}
		}
		if t := pick("image/"); t != "" {
			b, err := exec.CommandContext(ctx, "wl-paste", "--type", t).Output()
			if err != nil || len(b) == 0 {
				return clipboardMsg{err: fmt.Errorf("reading %s from the clipboard failed", t)}
			}
			ext := map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp"}[t]
			if ext == "" {
				ext = ".png"
			}
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return clipboardMsg{err: err}
			}
			p := filepath.Join(dir, "clipboard-"+time.Now().Format("20060102-150405")+ext)
			if err := os.WriteFile(p, b, 0o600); err != nil {
				return clipboardMsg{err: err}
			}
			return clipboardMsg{files: []string{p}}
		}
		b, err := exec.CommandContext(ctx, "wl-paste", "--no-newline").Output()
		if err != nil {
			return clipboardMsg{err: errors.New("nothing pasteable on the clipboard")}
		}
		return clipboardMsg{text: string(bytes.ToValidUTF8(b, nil))}
	}
}
