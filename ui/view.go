package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/laddhebert/tinwire/imsg"
	"github.com/laddhebert/tinwire/remote"
)

var (
	dim        = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	meStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true)
	themStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("5")).Bold(true)
	unreadDot  = lipgloss.NewStyle().Foreground(lipgloss.Color("4")).Render("●")
	cursorBar  = lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Render("▌")
	selActive  = lipgloss.NewStyle().Reverse(true)
	selIdle    = lipgloss.NewStyle().Background(lipgloss.Color("236"))
	header     = lipgloss.NewStyle().Bold(true)
	errStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	okStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	paneSep    = lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
	attachTag  = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	openChat   = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	unreadTime = lipgloss.NewStyle().Foreground(lipgloss.Color("4")).Bold(true)
)

var reactionGlyph = map[string]string{
	"love": "♥", "like": "👍", "dislike": "👎", "laugh": "😂", "emphasis": "‼", "question": "?",
}

func reactionGlyphFor(msg imsg.Message) string {
	if msg.ReactionEmoji != "" {
		return msg.ReactionEmoji
	}
	if g := reactionGlyph[msg.ReactionType]; g != "" {
		return g
	}
	return msg.ReactionType
}

func (m Model) dims() (listW, convW, bodyH, vpH int) {
	listW = m.w / 3
	if listW > 36 {
		listW = 36
	}
	if listW < 18 {
		listW = 18
	}
	convW = m.w - listW - 1
	bodyH = m.h - 1
	vpH = bodyH - 1 /*header*/ - 1 /*rule or attachment line*/ - inputLines
	return
}

func (m *Model) layout() {
	_, convW, _, vpH := m.dims()
	m.vp.Width = max(1, convW)
	m.vp.Height = max(1, vpH)
	m.ta.SetWidth(max(1, convW))
	m.to.Width = max(1, convW-lipgloss.Width(m.to.Prompt)-1)
	m.at.Width = max(1, convW-lipgloss.Width(m.at.Prompt)-1)
}

func (m *Model) refreshVP(stick bool) {
	_, convW, _, _ := m.dims()
	atBottom := m.vp.AtBottom()
	content, starts := m.renderConversation(max(10, convW))
	m.lineStarts = starts
	m.vpLines = strings.Split(content, "\n")
	m.attachLines = m.findAttachLines()
	m.vp.SetContent(content)
	if m.focus == focusConv || m.focus == focusReact {
		m.ensureCursorVisible()
		return
	}
	if stick || atBottom {
		m.vp.GotoBottom()
	}
}

func (m *Model) ensureCursorVisible() {
	if m.cursor < 0 || m.cursor >= len(m.lineStarts) {
		return
	}
	start := m.lineStarts[m.cursor]
	end := m.vp.TotalLineCount() - 1
	if m.cursor+1 < len(m.lineStarts) {
		end = m.lineStarts[m.cursor+1] - 1
	}
	switch {
	case start < m.vp.YOffset:
		m.vp.SetYOffset(start)
	case end >= m.vp.YOffset+m.vp.Height:
		m.vp.SetYOffset(max(start, end-m.vp.Height+1))
	}
}

func (m Model) View() string {
	if m.w == 0 {
		return "starting…"
	}
	if m.w < 50 || m.h < 10 {
		return "window too small"
	}
	listW, convW, bodyH, _ := m.dims()

	list := m.renderList(listW, bodyH)
	sep := paneSep.Render(strings.Repeat("│\n", bodyH-1) + "│")

	var conv strings.Builder
	conv.WriteString(m.renderHeader(convW) + "\n")
	conv.WriteString(m.viewportView() + "\n")
	conv.WriteString(m.renderComposeRule(convW) + "\n")
	switch m.focus {
	case focusTo:
		conv.WriteString(m.to.View() + strings.Repeat("\n", inputLines-1))
	case focusAttach:
		conv.WriteString(m.at.View() + "\n" + dim.Render("enter attach · esc cancel · ~ expands to your home") + strings.Repeat("\n", inputLines-2))
	default:
		conv.WriteString(m.ta.View())
	}
	right := lipgloss.NewStyle().Width(convW).Height(bodyH).MaxHeight(bodyH).Render(conv.String())

	body := lipgloss.JoinHorizontal(lipgloss.Top, list, sep, right)
	return body + "\n" + m.renderStatus()
}

func (m Model) renderComposeRule(w int) string {
	if len(m.attach) == 0 {
		return paneSep.Render(strings.Repeat("─", w))
	}
	var total int64
	names := make([]string, 0, len(m.attach))
	for _, p := range m.attach {
		names = append(names, filepath.Base(p))
		if st, err := os.Stat(p); err == nil {
			total += st.Size()
		}
	}
	label := "📎 " + strings.Join(names, ", ") + " (" + humanBytes(total) + ") · ctrl+x remove "
	if lipgloss.Width(label) > w-4 {
		label = fmt.Sprintf("📎 %d files (%s) · ctrl+x remove ", len(m.attach), humanBytes(total))
	}
	return truncate(attachTag.Render(label)+paneSep.Render(strings.Repeat("─", max(0, w-lipgloss.Width(label)))), w)
}

// syncListTop scrolls the chat list just enough to keep the selection visible.
func (m *Model) syncListTop() {
	_, _, h, _ := m.dims()
	if h <= 0 {
		return
	}
	if m.sel < m.listTop {
		m.listTop = m.sel
	}
	if m.sel >= m.listTop+h {
		m.listTop = m.sel - h + 1
	}
	if maxTop := max(0, len(m.chats)-h); m.listTop > maxTop {
		m.listTop = maxTop
	}
	if m.listTop < 0 {
		m.listTop = 0
	}
}

func (m Model) renderList(w, h int) string {
	var b strings.Builder
	rows := 0
	if len(m.chats) == 0 {
		b.WriteString(dim.Render(" no chats yet"))
		rows = 1
	}
	for i := m.listTop; i < len(m.chats) && rows < h; i++ {
		c := m.chats[i]
		mark := " "
		if c.UnreadCount > 0 {
			mark = unreadDot
		}
		ts := shortTime(c.LastMessageAt.Time)
		titleW := w - 2 - len(ts) - 1
		title := truncate(m.titleFor(c), titleW)
		pad := strings.Repeat(" ", max(0, titleW-lipgloss.Width(title)))

		// Unread chats are bold (name and time); the open chat is cyan.
		unread := c.UnreadCount > 0
		titleStyle, tsStyle := lipgloss.NewStyle(), dim
		if c.ID == m.open {
			titleStyle = openChat
		}
		if unread {
			titleStyle = titleStyle.Bold(true)
			tsStyle = unreadTime
		}
		line := " " + titleStyle.Render(title) + pad + " " + tsStyle.Render(ts)
		if i == m.sel {
			sel := selIdle
			if m.focus == focusList {
				sel = selActive
			}
			line = sel.Bold(unread).Render(" " + title + pad + " " + ts)
		}
		b.WriteString(mark + line)
		rows++
		if rows < h {
			b.WriteString("\n")
		}
	}
	return lipgloss.NewStyle().Width(w).Height(h).MaxHeight(h).Render(b.String())
}

func (m Model) renderHeader(w int) string {
	switch {
	case m.open != 0:
		c, _ := m.chatByID(m.open)
		s := header.Render(m.titleFor(c))
		if c.Service != "" {
			s += dim.Render(" · " + c.Service)
		}
		if c.IsGroup && len(c.Participants) > 0 {
			names := make([]string, 0, len(c.Participants))
			for _, p := range c.Participants {
				names = append(names, m.nameFor(p))
			}
			s += dim.Render(" · " + strings.Join(names, ", "))
		} else if len(c.Participants) == 1 && m.nameFor(c.Participants[0]) != c.Participants[0] {
			s += dim.Render(" · " + c.Participants[0])
		}
		return truncate(s, w)
	case m.openTo != "":
		return truncate(header.Render("New message to "+m.nameFor(m.openTo))+dim.Render(" · "+m.openTo), w)
	}
	return dim.Render("no chat open")
}

// renderConversation returns the viewport content and the first line of
// each message so the cursor can be scrolled into view.
func (m Model) renderConversation(w int) (string, []int) {
	if m.open == 0 {
		if m.openTo != "" {
			return dim.Render("New conversation. Type a message (ctrl+a to attach a file) and press enter to send it to " + m.openTo + "."), nil
		}
		return dim.Render(strings.Join([]string{
			"j/k or ↑/↓ to pick a chat, enter to open it.",
			"",
			"In the conversation (tab from the list, or ↑ from an empty compose box):",
			"  j/k          move between messages (k at the top loads older ones)",
			"  o            open attachment (images/video/audio/pdf; others are saved)",
			"  s            save attachment to " + m.cfg.DownloadDir,
			"  y            copy message text",
			"  r            tapback on the latest message",
			"  i / enter    back to compose",
			"",
			"Compose:",
			"  enter        send · alt+enter newline · ctrl+a attach · ctrl+v paste image · ctrl+x drop attachments",
			"  drag files onto the window to attach them",
			"",
			"ctrl+n new conversation · ctrl+r refresh/reconnect · pgup/pgdn scroll · q/ctrl+c quit",
		}, "\n")), nil
	}
	if m.loading[m.open] && len(m.msgs[m.open]) == 0 {
		return dim.Render("loading…"), nil
	}
	c, _ := m.chatByID(m.open)
	list := m.msgs[m.open]
	showCursor := m.focus == focusConv || m.focus == focusReact
	var b strings.Builder
	starts := make([]int, len(list))
	line := 0
	if m.exhausted[m.open] {
		b.WriteString(dim.Render("── beginning of conversation ──") + "\n")
		line++
	} else if m.older[m.open] {
		b.WriteString(dim.Render("── loading older… ──") + "\n")
		line++
	}
	var lastDay string
	for i, msg := range list {
		if d := msg.CreatedAt.Local().Format("Mon Jan 2 2006"); d != lastDay && !msg.CreatedAt.IsZero() {
			lastDay = d
			b.WriteString(dim.Render("── "+d+" ──") + "\n")
			line++
		}
		starts[i] = line
		r := m.renderMessage(msg, c, w-1)
		bar := " "
		if showCursor && i == m.cursor {
			bar = cursorBar
		}
		rows := strings.Split(r, "\n")
		for j := range rows {
			rows[j] = bar + rows[j]
		}
		b.WriteString(strings.Join(rows, "\n") + "\n")
		line += len(rows)
	}
	if len(list) == 0 {
		return dim.Render("no messages"), nil
	}
	return strings.TrimRight(b.String(), "\n"), starts
}

func (m Model) renderMessage(msg imsg.Message, c imsg.Chat, w int) string {
	ts := "     "
	if !msg.CreatedAt.IsZero() {
		ts = msg.CreatedAt.Local().Format("15:04")
	}
	var name string
	if msg.IsFromMe {
		name = meStyle.Render("me")
	} else {
		n := msg.SenderName
		if msg.Sender != "" {
			if r := m.nameFor(msg.Sender); r != msg.Sender || n == "" {
				n = r
			}
		}
		if n == "" {
			n = m.titleFor(c)
		}
		name = themStyle.Render(n)
	}
	prefix := dim.Render(ts) + " " + name + " "
	bodyW := max(10, w-lipgloss.Width(prefix))

	var parts []string
	switch {
	case msg.Type == "unsent" || !msg.UnsentAt.IsZero():
		parts = append(parts, dim.Italic(true).Render("(unsent)"))
	case msg.IsReaction:
		verb := "reacted " + reactionGlyphFor(msg)
		if !msg.IsReactionAdd {
			verb = "removed " + reactionGlyphFor(msg)
		}
		if t := m.reactedText(msg.ReactedToGUID); t != "" {
			verb += " to “" + t + "”"
		}
		parts = append(parts, dim.Render(verb))
	default:
		if msg.ReplyToGUID != "" {
			if t := m.reactedText(msg.ReplyToGUID); t != "" {
				parts = append(parts, dim.Render("↳ “"+t+"”"))
			}
		}
		if t := strings.TrimSpace(msg.Text); t != "" {
			if !msg.EditedAt.IsZero() {
				t += dim.Render(" (edited)")
			}
			parts = append(parts, t)
		}
		for _, a := range msg.Attachments {
			l := "📎 " + a.Label()
			if a.TotalBytes > 0 {
				l += " (" + humanBytes(a.TotalBytes) + ")"
			}
			hint := " · click to open"
			if a.Missing {
				l += " [not on the Mac]"
				hint = ""
			}
			// One line per attachment so a click maps to exactly one file.
			l = truncate(l, max(4, bodyW-lipgloss.Width(hint)))
			parts = append(parts, attachTag.Render(l)+dim.Render(hint))
		}
		if len(parts) == 0 {
			parts = append(parts, dim.Render("(no text)"))
		}
	}
	body := lipgloss.NewStyle().Width(bodyW).Render(strings.Join(parts, "\n"))
	return lipgloss.JoinHorizontal(lipgloss.Top, prefix, body)
}

// reactedText returns a short excerpt of the message with the given GUID in
// the open chat, for tapback and reply context.
func (m Model) reactedText(guid string) string {
	if guid == "" {
		return ""
	}
	// Reaction targets may carry a part prefix such as "p:0/GUID".
	if i := strings.LastIndex(guid, "/"); i >= 0 {
		guid = guid[i+1:]
	}
	for _, x := range m.msgs[m.open] {
		if x.GUID == guid {
			t := strings.Join(strings.Fields(x.Text), " ")
			if t == "" && len(x.Attachments) > 0 {
				t = "📎 " + x.Attachments[0].Label()
			}
			if r := []rune(t); len(r) > 40 {
				t = string(r[:40]) + "…"
			}
			return t
		}
	}
	return ""
}

func (m Model) renderStatus() string {
	conn := errStyle.Render("●")
	if m.connected {
		conn = okStyle.Render("●")
	}
	flags := ""
	if m.cfg.DryRun {
		flags = " [dry-run]"
	}
	// Full and compact key hints per focus; the compact one is used when the
	// full one would squeeze the status message below 24 columns.
	var hint, short string
	switch m.focus {
	case focusList:
		hint, short = "↵ open · tab messages · ctrl+n new · q quit", "↵ open · ctrl+n new"
	case focusConv:
		hint, short = "j/k move · o open · s save · y copy · drag to select · r react · i compose", "o open · y copy · r react"
	case focusInput:
		hint, short = "↵ send · alt+↵ newline · ctrl+a attach · ctrl+v paste · tab messages · esc list", "↵ send · ctrl+a attach"
	case focusReact:
		hint, short = "1 ♥ 2 👍 3 👎 4 😂 5 ‼ 6 ? · esc", "1-6 react · esc"
	default:
		hint, short = "↵ confirm · esc cancel", "↵ · esc"
	}
	if m.w-lipgloss.Width(hint)-2 < 24 {
		hint = short
	}
	st := m.status
	if m.statusErr {
		st = errStyle.Render(st)
	}
	left := conn + flags + " " + st
	// Errors get the whole bar: a truncated error is useless. Otherwise keep the
	// key hints visible by truncating the status message to make room.
	if m.statusErr && lipgloss.Width(left)+lipgloss.Width(hint)+2 > m.w {
		return truncate(left, m.w)
	}
	hintW := lipgloss.Width(hint)
	if room := m.w - hintW - 2; room >= 24 {
		if lipgloss.Width(left) > room {
			left = truncate(left, room)
		}
		return left + strings.Repeat(" ", max(1, m.w-lipgloss.Width(left)-hintW)) + dim.Render(hint)
	}
	return truncate(left, m.w)
}

func (m Model) chatByID(id int64) (imsg.Chat, bool) {
	for _, c := range m.chats {
		if c.ID == id {
			return c, true
		}
	}
	return imsg.Chat{ID: id}, false
}

func (m Model) chatTitle(id int64) string {
	c, _ := m.chatByID(id)
	return m.titleFor(c)
}

// nameFor resolves a handle: alias file first, then the Mac's AddressBook.
func (m Model) nameFor(h string) string {
	if a, ok := m.cfg.Aliases[h]; ok {
		return a
	}
	for _, k := range remote.HandleKeys(h) {
		if a, ok := m.cfg.Aliases[k]; ok {
			return a
		}
		if n, ok := m.contacts[k]; ok {
			return n
		}
	}
	return h
}

func (m Model) titleFor(c imsg.Chat) string {
	if c.DisplayName != "" {
		return c.DisplayName
	}
	if !c.IsGroup {
		for _, h := range append(append([]string{}, c.Participants...), c.Identifier) {
			if h == "" {
				continue
			}
			if n := m.nameFor(h); n != h {
				return n
			}
		}
	}
	switch {
	case c.ContactName != "":
		return c.ContactName
	case c.IsGroup && len(c.Participants) > 0:
		names := make([]string, 0, len(c.Participants))
		for _, p := range c.Participants {
			names = append(names, firstName(m.nameFor(p)))
		}
		return strings.Join(names, ", ")
	case c.Name != "":
		return c.Name
	case c.Identifier != "":
		return c.Identifier
	}
	return fmt.Sprintf("chat %d", c.ID)
}

// firstName shortens "Jane Appleseed" to "Jane" for group titles, but leaves
// raw handles alone.
func firstName(s string) string {
	if strings.ContainsAny(s, "@+") {
		return s
	}
	if f := strings.Fields(s); len(f) > 1 {
		return f[0]
	}
	return s
}

func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	return lipgloss.NewStyle().MaxWidth(w-1).Render(s) + "…"
}

func shortTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	t = t.Local()
	now := time.Now()
	switch {
	case t.Year() == now.Year() && t.YearDay() == now.YearDay():
		return t.Format("15:04")
	case now.Sub(t) < 6*24*time.Hour:
		return t.Format("Mon")
	default:
		return t.Format("01/02")
	}
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	}
	return fmt.Sprintf("%d B", n)
}
