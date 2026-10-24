// Package ui is the Bubble Tea front end: chat list on the left, the open
// conversation (with a message cursor) on the right, a compose box below it,
// and a status bar.
package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/laddhebert/tinwire/imsg"
	"github.com/laddhebert/tinwire/remote"
	"github.com/laddhebert/tinwire/rpc"
)

// Config is everything the UI needs from main.
type Config struct {
	Connect      func() (*imsg.API, error)
	Mac          remote.Mac
	Host         string
	ChatLimit    int
	HistoryLimit int
	MaxSends     int // per run; 0 = unlimited
	DryRun       bool
	Aliases      map[string]string // handle -> name; overrides Contacts
	Contacts     bool              // read names from the Mac's AddressBook
	Notify       bool              // desktop notifications via notify-send
	MarkRead     bool              // mark chats read on the Mac when viewed
	Typing       bool              // send typing indicators
	CacheDir     string            // downloaded attachments
	DownloadDir  string            // where 's' saves attachments
	MaxAttach    int64             // largest file accepted for sending
	StateDir     string            // local state (seen.json); empty = in memory only
}

type focus int

const (
	focusList focus = iota
	focusConv
	focusInput
	focusTo
	focusAttach
	focusReact
)

const inputLines = 3

var reactions = []struct{ key, kind, glyph string }{
	{"1", "love", "♥"}, {"2", "like", "👍"}, {"3", "dislike", "👎"},
	{"4", "laugh", "😂"}, {"5", "emphasis", "‼"}, {"6", "question", "?"},
}

// Extensions that are opened directly. Anything else a sender can name (a
// .desktop file, a script) is only saved, never handed to xdg-open.
var openable = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true, ".bmp": true, ".tiff": true,
	".mp4": true, ".mov": true, ".m4v": true, ".webm": true,
	".m4a": true, ".mp3": true, ".aac": true, ".wav": true, ".ogg": true, ".opus": true,
	".pdf": true, ".txt": true,
}

type Model struct {
	cfg Config

	api       *imsg.API
	connected bool
	gen       int // connection generation; stale messages from old connections are ignored
	backoff   time.Duration

	chats   []imsg.Chat
	sel     int
	listTop int

	open        int64  // open chat id; 0 = none or a new conversation
	openTo      string // recipient of a new conversation when open == 0
	pendingOpen string // after a SendTo, open the chat for this handle once it appears

	msgs        map[int64][]imsg.Message
	loading     map[int64]bool
	older       map[int64]bool    // older page in flight
	exhausted   map[int64]bool    // no older history left
	cursor      int               // index into msgs[open] while focusConv
	lineStarts  []int             // first viewport line of each message, from the last render
	vpLines     []string          // the conversation content, one entry per line
	attachLines map[int]attachRef // content line -> the attachment drawn on it
	pressLine   int               // content line of the last left press in the conversation
	seen        *seenStore        // chats viewed in tinwire (persists unread state locally)

	// drag-to-select (selection.go)
	selecting, dragged, hasSel bool
	selA, selB                 textPos

	contacts map[string]string

	vp viewport.Model
	ta textarea.Model
	to textinput.Model
	at textinput.Model // attach path prompt

	focus       focus
	termFocused bool
	w, h        int

	attach []string // local files queued for the next send (ctrl+a, drop, ctrl+v)

	compress      *compressJob // running video compression (video.go)
	compressQueue []string
	compressSeq   int
	compressPct   int
	sendWhenReady bool // enter was pressed during compression: send once attached
	sends         int
	sending       bool
	laneBlocked   bool

	typingOn  bool
	typingFor int64
	typingSeq int
	lastKey   time.Time
	typingOK  bool
	readOK    bool
	notifier  string

	status    string
	statusErr bool
}

func New(cfg Config) Model {
	ta := textarea.New()
	ta.Placeholder = "Message"
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	ta.CharLimit = 0
	ta.SetHeight(inputLines)
	// Shift/ctrl+Enter are rewritten to alt+enter by package keyfix.
	ta.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("alt+enter", "ctrl+j"))
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()

	to := textinput.New()
	to.Placeholder = "+15551234567, name@example.com, or a contact name"
	to.Prompt = "To: "

	at := textinput.New()
	at.Placeholder = "~/Pictures/photo.jpg"
	at.Prompt = "Attach: "

	notifier := ""
	if cfg.Notify {
		notifier, _ = exec.LookPath("notify-send")
	}
	if cfg.MaxAttach == 0 {
		cfg.MaxAttach = 100 << 20
	}

	return Model{
		cfg:         cfg,
		gen:         1,
		backoff:     2 * time.Second,
		msgs:        map[int64][]imsg.Message{},
		loading:     map[int64]bool{},
		older:       map[int64]bool{},
		exhausted:   map[int64]bool{},
		cursor:      -1,
		vp:          viewport.New(0, 0),
		ta:          ta,
		to:          to,
		at:          at,
		termFocused: true,
		typingOK:    cfg.Typing,
		readOK:      cfg.MarkRead,
		notifier:    notifier,
		seen:        loadSeen(cfg.StateDir),
		status:      "connecting to " + cfg.Host + "…",
	}
}

// ---- messages ----

type connectedMsg struct {
	api    *imsg.API
	status imsg.Status
	gen    int
}
type connectErrMsg struct {
	err error
	gen int
}
type disconnectedMsg struct {
	err error
	gen int
}
type reconnectMsg struct{ gen int }
type chatsMsg struct {
	chats []imsg.Chat
	err   error
}
type historyMsg struct {
	chatID int64
	msgs   []imsg.Message
	err    error
}
type olderMsg struct {
	chatID int64
	msgs   []imsg.Message
	err    error
}
type noteMsg struct {
	n   rpc.Notification
	gen int
}
type sentMsg struct {
	chatID int64
	to     string
	text   string
	unsent []string // files not sent (restored to the compose box on a safe failure)
	files  []string // every file this send tried
	sent   int      // files that did go out before an error
	res    imsg.SendResult
	err    error
}
type contactsMsg struct {
	names map[string]string
	err   error
}
type typingTickMsg struct{ seq int }
type typingErrMsg struct{ err error }
type readDoneMsg struct {
	chatID int64
	err    error
}

// actionMsg reports the outcome of a side action (open, save, copy, react).
type actionMsg struct {
	text string
	err  error
}

// ---- commands ----

func (m Model) connectCmd(gen int) tea.Cmd {
	return func() tea.Msg {
		api, err := m.cfg.Connect()
		if err != nil {
			return connectErrMsg{err, gen}
		}
		st, err := api.Status()
		if err != nil {
			api.C.Close()
			return connectErrMsg{fmt.Errorf("status: %w", err), gen}
		}
		if !st.Database.Ready {
			api.C.Close()
			return connectErrMsg{fmt.Errorf("Messages database not readable on the Mac (Full Disk Access?): %s", st.Database.Error), gen}
		}
		if _, err := api.Subscribe(); err != nil {
			api.C.Close()
			return connectErrMsg{fmt.Errorf("watch.subscribe: %w", err), gen}
		}
		return connectedMsg{api, st, gen}
	}
}

func waitNote(api *imsg.API, gen int) tea.Cmd {
	return func() tea.Msg {
		n, ok := <-api.C.Notifications()
		if !ok {
			<-api.C.Done()
			return disconnectedMsg{api.C.Err(), gen}
		}
		return noteMsg{n, gen}
	}
}

func (m Model) loadChats() tea.Cmd {
	api, limit := m.api, m.cfg.ChatLimit
	if api == nil {
		return nil
	}
	return func() tea.Msg {
		c, err := api.Chats(limit)
		return chatsMsg{c, err}
	}
}

func (m *Model) loadHistory(id int64) tea.Cmd {
	api, limit := m.api, m.cfg.HistoryLimit
	if api == nil || m.loading[id] {
		return nil
	}
	m.loading[id] = true
	return func() tea.Msg {
		ms, err := api.History(id, limit)
		return historyMsg{id, ms, err}
	}
}

func (m *Model) loadOlder(id int64) tea.Cmd {
	list := m.msgs[id]
	if m.api == nil || m.older[id] || m.exhausted[id] || len(list) == 0 {
		return nil
	}
	m.older[id] = true
	api, limit, before := m.api, m.cfg.HistoryLimit, list[0].CreatedAt.Time
	m.setStatus("loading older messages…", false)
	return func() tea.Msg {
		ms, err := api.HistoryBefore(id, limit, before)
		return olderMsg{id, ms, err}
	}
}

func (m Model) loadContacts() tea.Cmd {
	if !m.cfg.Contacts {
		return nil
	}
	mac := m.cfg.Mac
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		names, err := mac.Contacts(ctx)
		return contactsMsg{names, err}
	}
}

func (m Model) markRead(id int64) tea.Cmd {
	if !m.readOK || m.api == nil || id == 0 || m.cfg.DryRun {
		return nil
	}
	api := m.api
	return func() tea.Msg { return readDoneMsg{id, api.Read(id)} }
}

func (m Model) setTyping(id int64, on bool) tea.Cmd {
	if m.api == nil || id == 0 || m.cfg.DryRun {
		return nil
	}
	api := m.api
	return func() tea.Msg {
		if err := api.Typing(id, on); err != nil {
			return typingErrMsg{err}
		}
		return nil
	}
}

// stopTyping clears an active typing indicator, if any.
func (m *Model) stopTyping() tea.Cmd {
	if !m.typingOn {
		return nil
	}
	m.typingOn = false
	return m.setTyping(m.typingFor, false)
}

// ---- tea.Model ----

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.connectCmd(m.gen), textarea.Blink)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	nm, cmd := m.update(msg)
	mm := nm.(Model)
	mm.syncListTop() // keep the list scroll in the model so clicks map to rows
	return mm, cmd
}

func (m Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if cmd, ok := m.handleCompress(msg); ok {
		return m, cmd
	}
	switch msg := msg.(type) {
	case tea.MouseMsg:
		return m.handleMouse(msg)

	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.layout()
		m.refreshVP(false)
		return m, nil

	case tea.FocusMsg:
		m.termFocused = true
		if m.open != 0 && m.unread(m.open) > 0 {
			m.clearUnread(m.open)
			m.markSeen(m.open)
			return m, m.markRead(m.open)
		}
		return m, nil

	case tea.BlurMsg:
		m.termFocused = false
		return m, nil

	case connectedMsg:
		if msg.gen != m.gen {
			msg.api.C.Close()
			return m, nil
		}
		m.api, m.connected, m.backoff = msg.api, true, 2*time.Second
		m.setStatus(fmt.Sprintf("connected to %s · imsg %s", m.cfg.Host, msg.status.Version), false)
		cmds := []tea.Cmd{m.loadChats(), waitNote(m.api, m.gen)}
		if m.contacts == nil {
			cmds = append(cmds, m.loadContacts())
		}
		if m.open != 0 { // reconcile anything missed while disconnected
			delete(m.loading, m.open)
			cmds = append(cmds, m.loadHistory(m.open))
		}
		return m, tea.Batch(cmds...)

	case connectErrMsg:
		if msg.gen != m.gen {
			return m, nil
		}
		return m, m.scheduleReconnect(msg.err)

	case disconnectedMsg:
		if msg.gen != m.gen {
			return m, nil
		}
		m.api, m.connected, m.sending, m.typingOn = nil, false, false, false
		m.gen++
		m.loading = map[int64]bool{}
		m.older = map[int64]bool{}
		return m, m.scheduleReconnect(fmt.Errorf("disconnected: %v", msg.err))

	case reconnectMsg:
		if msg.gen != m.gen || m.connected {
			return m, nil
		}
		m.setStatus("reconnecting to "+m.cfg.Host+"…", false)
		return m, m.connectCmd(m.gen)

	case contactsMsg:
		if msg.err != nil {
			m.contacts = map[string]string{}
			m.setStatus("contact names unavailable: "+msg.err.Error(), true)
			return m, nil
		}
		m.contacts = msg.names
		m.refreshVP(false)
		return m, nil

	case chatsMsg:
		if msg.err != nil {
			m.setStatus("chats: "+msg.err.Error(), true)
			return m, nil
		}
		m.mergeChats(msg.chats)
		if m.pendingOpen != "" {
			if i := m.findHandle(m.pendingOpen); i >= 0 {
				m.pendingOpen = ""
				m.sel = i
				return m, m.openChat(m.chats[i].ID)
			}
		}
		return m, nil

	case historyMsg:
		delete(m.loading, msg.chatID)
		if msg.err != nil {
			m.setStatus("history: "+msg.err.Error(), true)
			return m, nil
		}
		sortMsgs(msg.msgs)
		m.msgs[msg.chatID] = msg.msgs
		if msg.chatID == m.open {
			if m.focus == focusConv {
				m.cursor = len(msg.msgs) - 1
			}
			m.refreshVP(true)
		}
		return m, nil

	case olderMsg:
		delete(m.older, msg.chatID)
		if msg.err != nil {
			m.setStatus("older history: "+msg.err.Error(), true)
			return m, nil
		}
		list := m.msgs[msg.chatID]
		seen := map[int64]bool{}
		for _, x := range list {
			seen[x.ID] = true
		}
		var add []imsg.Message
		for _, x := range msg.msgs {
			if !seen[x.ID] {
				add = append(add, x)
			}
		}
		if len(add) == 0 {
			m.exhausted[msg.chatID] = true
			m.setStatus("beginning of conversation", false)
			return m, nil
		}
		sortMsgs(add)
		m.msgs[msg.chatID] = append(add, list...)
		if msg.chatID == m.open && m.cursor >= 0 {
			m.cursor += len(add)
		}
		m.setStatus(fmt.Sprintf("loaded %d older messages", len(add)), false)
		m.refreshVP(false)
		return m, nil

	case noteMsg:
		if msg.gen != m.gen || m.api == nil {
			return m, nil
		}
		cmd := m.handleNote(msg.n)
		return m, tea.Batch(cmd, waitNote(m.api, m.gen))

	case sentMsg:
		m.sending = false
		if msg.err != nil {
			switch imsg.Code(msg.err) {
			case imsg.CodeDeliveryUnknown:
				m.setStatus("delivery unknown: may have been sent, NOT retried. Check the thread before resending.", true)
			case imsg.CodeLaneBlocked:
				m.laneBlocked = true
				m.setStatus("send lane blocked by an earlier uncertain send: ctrl+r restarts imsg to clear it", true)
			default:
				m.setStatus("send failed: "+msg.err.Error(), true)
				if retrySafe(msg.err) || errors.Is(msg.err, errNotStarted) {
					if strings.TrimSpace(m.ta.Value()) == "" && msg.text != "" {
						m.ta.SetValue(msg.text) // provably not sent: give it back
					}
					if len(m.attach) == 0 && len(msg.unsent) > 0 {
						m.attach = msg.unsent
						m.layout()
					}
				}
			}
			return m, nil
		}
		m.setStatus(fmt.Sprintf("sent (%d this session)", m.sends), false)
		m.cleanupOutgoing(msg.files) // compressed copies are no longer needed
		if msg.to != "" {
			m.pendingOpen = msg.to
			return m, m.loadChats()
		}
		return m, nil

	case typingTickMsg:
		if msg.seq != m.typingSeq || !m.typingOn {
			return m, nil
		}
		if time.Since(m.lastKey) >= 5*time.Second {
			return m, m.stopTyping()
		}
		return m, m.typingTick()

	case typingErrMsg:
		if m.typingOK {
			m.typingOK, m.typingOn = false, false
			m.setStatus("typing indicators off for this session (Messages refused: "+shortErr(msg.err)+")", true)
		}
		return m, nil

	case readDoneMsg:
		if msg.err != nil && m.readOK {
			m.readOK = false
			m.setStatus("chats are marked read here; marking them read on the Mac/phone needs imsg's SIP-off bridge", false)
		}
		return m, nil

	case clipboardMsg:
		switch {
		case msg.err != nil:
			m.setStatus(msg.err.Error(), true)
		case len(msg.files) > 0:
			return m, m.addAttachments(msg.files)
		case msg.text != "" && m.focus == focusInput:
			m.ta.InsertString(msg.text)
			return m, m.noteTyping()
		}
		return m, nil

	case actionMsg:
		if msg.err != nil {
			m.setStatus(msg.err.Error(), true)
		} else {
			m.setStatus(msg.text, false)
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	// Pass anything else (cursor blink etc.) to the focused input.
	var cmd tea.Cmd
	switch m.focus {
	case focusInput:
		m.ta, cmd = m.ta.Update(msg)
	case focusTo:
		m.to, cmd = m.to.Update(msg)
	case focusAttach:
		m.at, cmd = m.at.Update(msg)
	}
	return m, cmd
}

func (m *Model) scheduleReconnect(err error) tea.Cmd {
	d := m.backoff
	if m.backoff < time.Minute {
		m.backoff *= 2
	}
	m.setStatus(fmt.Sprintf("%v · retrying in %s", err, d), true)
	gen := m.gen
	return tea.Tick(d, func(time.Time) tea.Msg { return reconnectMsg{gen} })
}

func (m *Model) typingTick() tea.Cmd {
	seq := m.typingSeq
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return typingTickMsg{seq} })
}

// ---- keys ----

func (m Model) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if k.Paste || len(k.Runes) > 1 || k.Type != tea.KeyRunes {
		debugf("key: type=%d paste=%v runes=%d focus=%d open=%d", k.Type, k.Paste, len(k.Runes), m.focus, m.open)
	}
	m.clearSelection() // any key dismisses a text selection
	switch k.String() {
	case "ctrl+c":
		return m.quit()
	case "ctrl+x":
		// Cancelling a video compression works from anywhere; otherwise ctrl+x
		// falls through to the compose box (remove attachments).
		if m.cancelCompress() {
			m.setStatus("cancelling video compression…", false)
			return m, nil
		}
	case "ctrl+r":
		return m.refresh()
	case "ctrl+n":
		cmd := m.stopTyping()
		m.focus = focusTo
		m.ta.Blur()
		m.to.Reset()
		return m, tea.Batch(cmd, m.to.Focus())
	case "pgup", "ctrl+u":
		m.vp.HalfViewUp()
		if m.vp.AtTop() && m.open != 0 {
			return m, m.loadOlder(m.open)
		}
		return m, nil
	case "pgdown", "ctrl+d":
		m.vp.HalfViewDown()
		return m, nil
	}

	switch m.focus {
	case focusList:
		return m.keyList(k)
	case focusConv:
		return m.keyConv(k)
	case focusInput:
		return m.keyInput(k)
	case focusTo:
		return m.keyTo(k)
	case focusAttach:
		return m.keyAttach(k)
	case focusReact:
		return m.keyReact(k)
	}
	return m, nil
}

func (m Model) quit() (tea.Model, tea.Cmd) {
	if m.typingOn && m.api != nil {
		_ = m.api.Typing(m.typingFor, false) // synchronous: we are exiting
	}
	if m.api != nil {
		m.api.C.Close()
	}
	return m, tea.Quit
}

func (m Model) refresh() (tea.Model, tea.Cmd) {
	if !m.connected {
		m.backoff = 2 * time.Second
		m.setStatus("reconnecting to "+m.cfg.Host+"…", false)
		return m, m.connectCmd(m.gen)
	}
	if m.laneBlocked {
		// Only a fresh `imsg rpc` child clears a poisoned send lane. Closing
		// the client ends the notification pump, which triggers a reconnect.
		m.laneBlocked = false
		m.setStatus("restarting imsg rpc to clear the send lane…", false)
		m.api.C.Close()
		return m, nil
	}
	cmds := []tea.Cmd{m.loadChats()}
	if m.open != 0 {
		delete(m.loading, m.open)
		delete(m.exhausted, m.open)
		cmds = append(cmds, m.loadHistory(m.open))
	}
	m.setStatus("refreshing…", false)
	return m, tea.Batch(cmds...)
}

func (m Model) keyList(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if cmd, ok := m.handlePaste(k); ok {
		return m, cmd
	}
	switch k.String() {
	case "q":
		return m.quit()
	case "up", "k":
		if m.sel > 0 {
			m.sel--
		}
	case "down", "j":
		if m.sel < len(m.chats)-1 {
			m.sel++
		}
	case "home", "g":
		m.sel = 0
	case "end", "G":
		m.sel = max(0, len(m.chats)-1)
	case "enter", "right", "l":
		if m.sel < len(m.chats) {
			return m, m.openChat(m.chats[m.sel].ID)
		}
	case "tab":
		if m.open != 0 {
			return m, m.focusConversation()
		}
		if m.openTo != "" {
			m.focus = focusInput
			return m, m.ta.Focus()
		}
	}
	return m, nil
}

func (m *Model) focusConversation() tea.Cmd {
	m.focus = focusConv
	m.ta.Blur()
	n := len(m.msgs[m.open])
	if m.cursor < 0 || m.cursor >= n {
		m.cursor = n - 1
	}
	m.refreshVP(false)
	return nil
}

func (m Model) keyConv(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if cmd, ok := m.handlePaste(k); ok {
		return m, cmd
	}
	list := m.msgs[m.open]
	switch k.String() {
	case "esc", "left", "h":
		m.focus = focusList
		m.refreshVP(false)
		return m, nil
	case "enter":
		// Enter opens a selected message's attachments; otherwise goes to compose.
		if m.cursor >= 0 && m.cursor < len(list) && len(list[m.cursor].Attachments) > 0 {
			return m, m.openAttachments(list[m.cursor], true)
		}
		m.focus = focusInput
		m.refreshVP(true)
		return m, m.ta.Focus()
	case "tab", "i":
		m.focus = focusInput
		m.refreshVP(true)
		return m, m.ta.Focus()
	case "q":
		return m.quit()
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
			m.refreshVP(false)
			return m, nil
		}
		return m, m.loadOlder(m.open)
	case "down", "j":
		if m.cursor < len(list)-1 {
			m.cursor++
			m.refreshVP(false)
		}
		return m, nil
	case "g", "home":
		m.cursor = 0
		m.refreshVP(false)
		return m, m.loadOlder(m.open)
	case "G", "end":
		m.cursor = len(list) - 1
		m.refreshVP(false)
		return m, nil
	}
	if m.cursor < 0 || m.cursor >= len(list) {
		return m, nil
	}
	sel := list[m.cursor]
	switch k.String() {
	case "o":
		return m, m.openAttachments(sel, true)
	case "s":
		return m, m.openAttachments(sel, false)
	case "y":
		return m, copyText(sel.Text)
	case "r":
		if !m.isLatest(m.cursor) {
			m.setStatus("tapbacks can only go on the latest message (Messages UI automation; targeting others needs the SIP-off bridge)", true)
			return m, nil
		}
		m.focus = focusReact
		m.setStatus("react: 1 ♥  2 👍  3 👎  4 😂  5 ‼  6 ?   esc cancel", false)
		return m, nil
	}
	return m, nil
}

func (m Model) keyInput(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if cmd, ok := m.handlePaste(k); ok {
		return m, cmd
	}
	switch k.String() {
	case "esc":
		cmd := m.stopTyping()
		m.focus = focusList
		m.ta.Blur()
		return m, cmd
	case "tab":
		cmd := m.stopTyping()
		if m.open != 0 {
			return m, tea.Batch(cmd, m.focusConversation())
		}
		m.focus = focusList
		m.ta.Blur()
		return m, cmd
	case "enter":
		return m, m.send()
	case "ctrl+a":
		m.focus = focusAttach
		m.ta.Blur()
		m.at.Reset()
		return m, m.at.Focus()
	case "ctrl+v":
		return m, m.pasteClipboard()
	case "ctrl+x":
		if m.cancelCompress() {
			m.setStatus("cancelling video compression…", false)
			return m, nil
		}
		if len(m.attach) > 0 {
			m.cleanupOutgoing(m.attach)
			m.attach = nil
			m.layout()
			m.setStatus("attachments removed", false)
		}
		return m, nil
	case "up":
		if strings.TrimSpace(m.ta.Value()) == "" && m.open != 0 {
			return m, m.focusConversation()
		}
	}
	before := m.ta.Value()
	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(k)
	if m.ta.Value() != before {
		return m, tea.Batch(cmd, m.noteTyping())
	}
	return m, cmd
}

// noteTyping starts the typing indicator on the first keystroke and keeps it
// alive; it is cleared 5 s after the last keystroke, on send, or on leaving.
func (m *Model) noteTyping() tea.Cmd {
	if !m.typingOK || m.open == 0 {
		return nil
	}
	m.lastKey = time.Now()
	if strings.TrimSpace(m.ta.Value()) == "" {
		return m.stopTyping()
	}
	if m.typingOn && m.typingFor == m.open {
		return nil
	}
	stop := m.stopTyping()
	m.typingOn, m.typingFor = true, m.open
	m.typingSeq++
	return tea.Batch(stop, m.setTyping(m.open, true), m.typingTick())
}

func (m Model) keyTo(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.focus = focusList
		m.to.Blur()
		return m, nil
	case "enter":
		to := strings.TrimSpace(m.to.Value())
		if to == "" {
			return m, nil
		}
		if h, ok := m.resolveName(to); ok {
			to = h
		}
		m.to.Blur()
		if i := m.findHandle(to); i >= 0 {
			m.sel = i
			return m, m.openChat(m.chats[i].ID)
		}
		m.open, m.openTo = 0, to
		m.focus = focusInput
		m.refreshVP(true)
		return m, m.ta.Focus()
	}
	var cmd tea.Cmd
	m.to, cmd = m.to.Update(k)
	return m, cmd
}

func (m Model) keyAttach(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	finish := func(paths []string) (tea.Model, tea.Cmd) {
		m.at.Blur()
		m.focus = focusInput
		cmd := m.addAttachments(paths)
		return m, tea.Batch(cmd, m.ta.Focus())
	}
	if k.Paste { // a file dropped onto the prompt
		if paths, ok := droppedFiles(string(k.Runes)); ok {
			return finish(paths)
		}
	}
	switch k.String() {
	case "esc":
		m.focus = focusInput
		m.at.Blur()
		return m, m.ta.Focus()
	case "enter":
		v := strings.TrimSpace(m.at.Value())
		if v == "" {
			return m, nil
		}
		paths, ok := droppedFiles(v)
		if !ok {
			paths = []string{expandHome(v)} // let checkFiles report what is wrong
		}
		return finish(paths)
	}
	var cmd tea.Cmd
	m.at, cmd = m.at.Update(k)
	return m, cmd
}

func (m Model) keyReact(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.focus = focusConv
	if k.String() == "esc" {
		m.setStatus("", false)
		return m, nil
	}
	for _, r := range reactions {
		if k.String() == r.key || k.String() == r.kind[:1] {
			if m.cfg.DryRun {
				m.setStatus("dry-run: would react "+r.glyph, false)
				return m, nil
			}
			mac, chatID, kind, glyph := m.cfg.Mac, m.open, r.kind, r.glyph
			m.setStatus("reacting "+glyph+"…", false)
			return m, func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
				defer cancel()
				if err := mac.React(ctx, chatID, kind); err != nil {
					return actionMsg{err: fmt.Errorf("tapback: %w", err)}
				}
				return actionMsg{text: "reacted " + glyph}
			}
		}
	}
	m.setStatus("no reaction sent", false)
	return m, nil
}

// ---- actions ----

func (m *Model) openChat(id int64) tea.Cmd {
	m.clearSelection()
	cmds := []tea.Cmd{m.stopTyping()}
	m.open, m.openTo = id, ""
	m.cursor = -1
	hadUnread := m.unread(id) > 0
	m.clearUnread(id)
	m.markSeen(id)
	m.focus = focusInput
	m.refreshVP(true)
	cmds = append(cmds, m.ta.Focus())
	if _, ok := m.msgs[id]; !ok {
		cmds = append(cmds, m.loadHistory(id))
	}
	if hadUnread {
		cmds = append(cmds, m.markRead(id))
	}
	return tea.Batch(cmds...)
}

var errNotStarted = errors.New("not started")

func (m *Model) send() tea.Cmd {
	text := strings.TrimSpace(m.ta.Value())
	files := append([]string(nil), m.attach...)
	switch {
	case m.compress != nil:
		m.sendWhenReady = true
		m.setStatus("will send as soon as the video is compressed (ctrl+x cancels)", false)
		return nil
	case text == "" && len(files) == 0:
		return nil
	case m.open == 0 && m.openTo == "":
		m.setStatus("open a chat first (enter on the list, or ctrl+n for a new one)", true)
		return nil
	}
	target := m.chatTitle(m.open)
	if m.open == 0 {
		target = m.openTo
	}
	what := fmt.Sprintf("%d chars", len([]rune(text)))
	switch len(files) {
	case 0:
	case 1:
		what += " + " + filepath.Base(files[0])
	default:
		what += fmt.Sprintf(" + %d files", len(files))
	}
	if m.cfg.DryRun { // checked first so dry-run works offline too
		m.ta.Reset()
		m.attach = nil
		m.layout()
		m.setStatus("dry-run: would send "+what+" to "+target, false)
		return nil
	}
	switch {
	case !m.connected || m.api == nil:
		m.setStatus("not connected; ctrl+r to reconnect", true)
		return nil
	case m.sending:
		m.setStatus("previous send still in flight", true)
		return nil
	case m.cfg.MaxSends > 0 && m.sends >= m.cfg.MaxSends:
		m.setStatus(fmt.Sprintf("send cap reached (--max-sends %d); restart to reset", m.cfg.MaxSends), true)
		return nil
	}
	m.sends++
	m.sending = true
	m.typingOn = false // Messages clears the indicator when the message lands
	m.ta.Reset()
	m.attach = nil
	m.layout()
	m.setStatus("sending "+what+" to "+target+"…", false)
	api, mac, chatID, to := m.api, m.cfg.Mac, m.open, m.openTo
	return func() tea.Msg {
		out := sentMsg{chatID: chatID, to: to, text: text, files: files}
		send := func(text, remoteFile string) (imsg.SendResult, error) {
			if chatID != 0 {
				return api.SendChat(chatID, text, remoteFile)
			}
			return api.SendTo(to, text, remoteFile)
		}
		// The text rides with the first file; each further file is its own
		// send (imsg sends one file per call).
		for i, f := range files {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			remoteFile, err := mac.Upload(ctx, f)
			cancel()
			if err != nil {
				out.unsent = files[i:]
				if i == 0 {
					out.err = fmt.Errorf("%w: %v", errNotStarted, err)
				} else {
					out.err = fmt.Errorf("sent %d of %d files, then upload failed: %w", i, len(files), err)
				}
				return out
			}
			caption := ""
			if i == 0 {
				caption = text
			}
			out.res, err = send(caption, remoteFile)
			ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
			_ = mac.RemoveUpload(ctx, remoteFile) // imsg staged its own copy
			cancel()
			if err != nil {
				out.err = err
				if i > 0 {
					out.text = "" // the caption already went out with the first file
				}
				if retrySafe(err) {
					out.unsent = files[i:]
				}
				return out
			}
			out.sent++
		}
		if len(files) == 0 {
			out.res, out.err = send(text, "")
		}
		return out
	}
}

// openAttachments downloads every attachment of msg into the cache. With
// open set, safe types are handed to xdg-open; otherwise (or for anything
// else) the files are copied to the download directory.
func (m *Model) openAttachments(msg imsg.Message, open bool) tea.Cmd {
	return m.openAttachmentsAt(msg, nil, open)
}

// openAttachmentsAt opens (or saves) the attachments at the given indexes;
// nil means all of them.
func (m *Model) openAttachmentsAt(msg imsg.Message, idxs []int, open bool) tea.Cmd {
	if len(msg.Attachments) == 0 {
		m.setStatus("no attachment on this message", true)
		return nil
	}
	atts := msg.Attachments
	if idxs != nil {
		atts = nil
		for _, i := range idxs {
			if i >= 0 && i < len(msg.Attachments) {
				atts = append(atts, msg.Attachments[i])
			}
		}
	}
	mac, cache, dl := m.cfg.Mac, m.cfg.CacheDir, m.cfg.DownloadDir
	m.setStatus("fetching attachment…", false)
	return func() tea.Msg {
		var done []string
		for _, a := range atts {
			if a.Missing || a.OriginalPath == "" {
				return actionMsg{err: fmt.Errorf("%s is not on the Mac (not downloaded from iCloud, or pruned)", a.Label())}
			}
			dir := filepath.Join(cache, remote.SafeName(msg.GUID))
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return actionMsg{err: err}
			}
			local, err := fetchCached(mac, a, dir)
			if err != nil {
				return actionMsg{err: err}
			}
			ext := strings.ToLower(filepath.Ext(local))
			if open && openable[ext] {
				if err := startDetached("xdg-open", local); err != nil {
					return actionMsg{err: fmt.Errorf("xdg-open: %w", err)}
				}
				done = append(done, "opened "+filepath.Base(local)+" in your default viewer")
				continue
			}
			saved, err := saveCopy(local, dl)
			if err != nil {
				return actionMsg{err: err}
			}
			note := "saved " + saved
			if open {
				note += " (" + ext + " files are not opened automatically)"
			}
			done = append(done, note)
		}
		return actionMsg{text: strings.Join(done, " · ")}
	}
}

func fetchCached(mac remote.Mac, a imsg.Attachment, dir string) (string, error) {
	base := filepath.Join(dir, remote.SafeName(a.Label()))
	// HEIC/CAF are cached under their converted extension (see remote.Download).
	cached := base
	switch strings.ToLower(filepath.Ext(base)) {
	case ".heic", ".heif":
		cached = strings.TrimSuffix(base, filepath.Ext(base)) + ".jpg"
	case ".caf":
		cached = strings.TrimSuffix(base, filepath.Ext(base)) + ".m4a"
	}
	if st, err := os.Stat(cached); err == nil && st.Size() > 0 {
		return cached, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	ext, err := mac.Download(ctx, a.OriginalPath, base)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(base, filepath.Ext(base)) + ext, nil
}

func saveCopy(src, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := filepath.Base(src)
	dst := filepath.Join(dir, name)
	for i := 1; ; i++ {
		if _, err := os.Stat(dst); os.IsNotExist(err) {
			break
		}
		dst = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(name, filepath.Ext(name)), i, filepath.Ext(name)))
	}
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return "", err
	}
	return dst, out.Close()
}

func startDetached(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

func copyText(text string) tea.Cmd {
	return func() tea.Msg {
		if strings.TrimSpace(text) == "" {
			return actionMsg{err: errors.New("nothing to copy")}
		}
		for _, c := range [][]string{{"wl-copy"}, {"xclip", "-selection", "clipboard"}, {"xsel", "-b", "-i"}} {
			if _, err := exec.LookPath(c[0]); err != nil {
				continue
			}
			cmd := exec.Command(c[0], c[1:]...)
			cmd.Stdin = strings.NewReader(text)
			if err := cmd.Run(); err != nil {
				return actionMsg{err: fmt.Errorf("%s: %w", c[0], err)}
			}
			return actionMsg{text: "copied to clipboard"}
		}
		return actionMsg{err: errors.New("no clipboard tool (wl-copy, xclip or xsel)")}
	}
}

func (m Model) notify(msg imsg.Message) tea.Cmd {
	if m.notifier == "" || msg.IsFromMe || msg.Type != "" {
		return nil
	}
	if msg.ChatID == m.open && m.termFocused {
		return nil
	}
	c, _ := m.chatByID(msg.ChatID)
	title := m.titleFor(c)
	if c.IsGroup {
		title = m.nameFor(msg.Sender) + " · " + title
	}
	body := msg.Text
	switch {
	case msg.IsReaction:
		body = "reacted " + reactionGlyphFor(msg)
	case body == "" && len(msg.Attachments) > 0:
		body = "📎 " + msg.Attachments[0].Label()
	}
	if r := []rune(body); len(r) > 200 {
		body = string(r[:200]) + "…"
	}
	notifier := m.notifier
	return func() tea.Msg {
		_ = exec.Command(notifier, "-a", "tinwire", "--", title, body).Run()
		return nil
	}
}

func (m *Model) handleNote(n rpc.Notification) tea.Cmd {
	switch n.Method {
	case "message":
		var w imsg.WatchNote
		if err := json.Unmarshal(n.Params, &w); err != nil {
			return nil
		}
		return m.applyIncoming(w.Message)
	case "watch.overflow":
		// Terminal for that subscription: resubscribe and reconcile.
		api := m.api
		m.setStatus("watch overflowed; resubscribing", true)
		cmds := []tea.Cmd{m.loadChats(), func() tea.Msg {
			if _, err := api.Subscribe(); err != nil {
				return chatsMsg{err: fmt.Errorf("resubscribe: %w", err)}
			}
			return nil
		}}
		if m.open != 0 {
			delete(m.loading, m.open)
			cmds = append(cmds, m.loadHistory(m.open))
		}
		return tea.Batch(cmds...)
	}
	return nil
}

func (m *Model) applyIncoming(msg imsg.Message) tea.Cmd {
	list, loaded := m.msgs[msg.ChatID]
	if msg.Type == "edited" || msg.Type == "unsent" {
		for i := range list {
			if list[i].ID == msg.ID {
				list[i] = msg
			}
		}
	} else if loaded {
		dup := false
		for i := range list {
			if list[i].ID == msg.ID {
				list[i], dup = msg, true
			}
		}
		if !dup {
			list = append(list, msg)
			sortMsgs(list)
		}
	}
	if loaded {
		m.msgs[msg.ChatID] = list
	}

	var cmds []tea.Cmd
	idx := -1
	for i := range m.chats {
		if m.chats[i].ID == msg.ChatID {
			idx = i
			break
		}
	}
	viewing := msg.ChatID == m.open && m.termFocused
	if idx < 0 {
		cmds = append(cmds, m.loadChats()) // a chat we have not listed yet
	} else if msg.Type == "" {
		c := m.chats[idx]
		if !msg.CreatedAt.IsZero() {
			c.LastMessageAt = msg.CreatedAt
		}
		if !msg.IsFromMe && !msg.IsReaction && !viewing {
			c.UnreadCount++
		}
		selID := int64(-1)
		if m.sel < len(m.chats) {
			selID = m.chats[m.sel].ID
		}
		m.chats = append(m.chats[:idx], m.chats[idx+1:]...)
		m.chats = append([]imsg.Chat{c}, m.chats...)
		m.keepSel(selID)
	}
	if msg.Type == "" && !msg.IsFromMe {
		cmds = append(cmds, m.notify(msg))
		if viewing && !msg.IsReaction {
			m.markSeen(msg.ChatID)
			cmds = append(cmds, m.markRead(msg.ChatID))
		}
	}
	if msg.ChatID == m.open {
		m.refreshVP(false)
	}
	return tea.Batch(cmds...)
}

// ---- state helpers ----

func (m *Model) mergeChats(chats []imsg.Chat) {
	selID := int64(-1)
	if m.sel < len(m.chats) {
		selID = m.chats[m.sel].ID
	}
	for i := range chats {
		if chats[i].ID == m.open || m.seen.covers(chats[i]) {
			chats[i].UnreadCount = 0
		}
	}
	m.chats = chats
	m.keepSel(selID)
}

func (m *Model) keepSel(id int64) {
	for i := range m.chats {
		if m.chats[i].ID == id {
			m.sel = i
			return
		}
	}
	if m.sel >= len(m.chats) {
		m.sel = max(0, len(m.chats)-1)
	}
}

func (m Model) unread(id int64) int {
	for _, c := range m.chats {
		if c.ID == id {
			return c.UnreadCount
		}
	}
	return 0
}

func (m *Model) clearUnread(id int64) {
	for i := range m.chats {
		if m.chats[i].ID == id {
			m.chats[i].UnreadCount = 0
		}
	}
}

// isLatest reports whether index i is the newest non-reaction message, the
// only one `imsg react` (Messages' last-message shortcut) can target.
func (m Model) isLatest(i int) bool {
	list := m.msgs[m.open]
	for j := len(list) - 1; j >= 0; j-- {
		if list[j].IsReaction || list[j].Type == "unsent" {
			continue
		}
		return j == i
	}
	return false
}

func (m Model) findHandle(h string) int {
	keys := remote.HandleKeys(h)
	for i, c := range m.chats {
		if c.IsGroup {
			continue
		}
		cands := append([]string{c.Identifier}, c.Participants...)
		for _, cand := range cands {
			if cand == h {
				return i
			}
			for _, a := range remote.HandleKeys(cand) {
				for _, b := range keys {
					if a == b {
						return i
					}
				}
			}
		}
	}
	return -1
}

// resolveName maps a typed contact name to a handle seen in the chat list.
func (m Model) resolveName(s string) (string, bool) {
	if strings.ContainsAny(s, "@+0123456789") {
		return "", false
	}
	want := strings.ToLower(s)
	for _, c := range m.chats {
		if c.IsGroup || len(c.Participants) != 1 {
			continue
		}
		if strings.ToLower(m.nameFor(c.Participants[0])) == want {
			return c.Participants[0], true
		}
	}
	return "", false
}

func (m *Model) setStatus(s string, isErr bool) {
	m.status, m.statusErr = s, isErr
}

func retrySafe(err error) bool {
	var re *rpc.Error
	if !errors.As(err, &re) || len(re.Data) == 0 {
		return false
	}
	var d struct {
		RetrySafe bool `json:"retry_safe"`
	}
	return json.Unmarshal(re.Data, &d) == nil && d.RetrySafe
}

func shortErr(err error) string {
	s := err.Error()
	var re *rpc.Error
	if errors.As(err, &re) && len(re.Data) > 0 {
		var d string
		if json.Unmarshal(re.Data, &d) == nil && d != "" {
			s = d
		}
	}
	if r := []rune(s); len(r) > 80 {
		s = string(r[:80]) + "…"
	}
	return s
}

func sortMsgs(ms []imsg.Message) {
	sort.SliceStable(ms, func(i, j int) bool {
		a, b := ms[i].CreatedAt.Time, ms[j].CreatedAt.Time
		if !a.Equal(b) {
			return a.Before(b)
		}
		return ms[i].ID < ms[j].ID
	})
}
