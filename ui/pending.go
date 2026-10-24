package ui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/laddhebert/tinwire/imsg"
)

// A send takes a few seconds before Messages records it (upload to the Mac,
// AppleScript, Messages processing the attachment), plus ~30 s to compress a
// big video. So every send shows up in the conversation at once as a
// placeholder ("⏳ sending…"); when Messages records the real message it
// replaces the placeholder, and its delivery state is then tracked
// ("✓ sent", "✓✓ delivered", "✗ not delivered") until it is final.

type pendingSend struct {
	id      int
	chatID  int64  // 0 for a new conversation
	to      string // recipient of a new conversation
	text    string
	files   []string
	started time.Time
	failed  string // non-empty: the send failed, shown until the next send in that chat
}

type deliveryTickMsg struct{}

type deliveryMsg struct{ states map[string]string }

const (
	deliveryPoll     = 2 * time.Second
	deliveryGiveUp   = 3 * time.Minute
	maxTrackedPerRun = 10
)

var (
	pendingStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Italic(true)
	deliveredStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	failedStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
)

func (m *Model) addPending(chatID int64, to, text string, files []string) int {
	m.pendingSeq++
	// A new send replaces earlier failures shown in the same chat.
	kept := m.pending[:0]
	for _, p := range m.pending {
		if !(p.failed != "" && p.chatID == chatID && p.to == to) {
			kept = append(kept, p)
		}
	}
	m.pending = append(kept, pendingSend{id: m.pendingSeq, chatID: chatID, to: to, text: text, files: files, started: time.Now()})
	return m.pendingSeq
}

// finishPending is called with the send's outcome.
func (m *Model) finishPending(id int, err error) {
	for i := range m.pending {
		if m.pending[i].id != id {
			continue
		}
		if err != nil {
			m.pending[i].failed = shortErr(err)
			return
		}
		m.pending = append(m.pending[:i], m.pending[i+1:]...)
		return
	}
}

// claimPending drops the oldest in-flight placeholder of a chat when the real
// outgoing message for it arrives from the watch.
func (m *Model) claimPending(chatID int64) {
	for i, p := range m.pending {
		if p.failed == "" && p.chatID == chatID {
			m.pending = append(m.pending[:i], m.pending[i+1:]...)
			return
		}
	}
}

// track starts following a sent message's delivery state.
func (m *Model) track(guid string) tea.Cmd {
	if guid == "" {
		return nil
	}
	if m.delivery == nil {
		m.delivery = map[string]deliveryState{}
	}
	if _, ok := m.delivery[guid]; ok {
		return nil
	}
	m.delivery[guid] = deliveryState{state: "sent", since: time.Now()}
	return m.scheduleDelivery()
}

type deliveryState struct {
	state string
	since time.Time
}

func final(state string) bool { return state == "delivered" || state == "failed" }

func (m *Model) scheduleDelivery() tea.Cmd {
	if m.deliveryTicking {
		return nil
	}
	m.deliveryTicking = true
	return tea.Tick(deliveryPoll, func(time.Time) tea.Msg { return deliveryTickMsg{} })
}

// pollDelivery asks Messages for the state of every message still in flight.
func (m *Model) pollDelivery() tea.Cmd {
	m.deliveryTicking = false
	if m.api == nil {
		return m.scheduleDelivery()
	}
	var guids []string
	for g, d := range m.delivery {
		if !final(d.state) && time.Since(d.since) < deliveryGiveUp && len(guids) < maxTrackedPerRun {
			guids = append(guids, g)
		}
	}
	if len(guids) == 0 {
		return nil
	}
	api := m.api
	return func() tea.Msg {
		states := map[string]string{}
		for _, g := range guids {
			if s, err := api.SendStatus(g); err == nil && s != "" {
				states[g] = s
			}
		}
		return deliveryMsg{states}
	}
}

func (m *Model) applyDelivery(msg deliveryMsg) tea.Cmd {
	changed := false
	for g, s := range msg.states {
		if d, ok := m.delivery[g]; ok && d.state != s {
			d.state = s
			m.delivery[g] = d
			changed = true
		}
	}
	if changed {
		m.refreshVP(false)
	}
	for _, d := range m.delivery {
		if !final(d.state) && time.Since(d.since) < deliveryGiveUp {
			return m.scheduleDelivery()
		}
	}
	return nil
}

// deliveryMark is the suffix shown after one of your own messages.
func (m Model) deliveryMark(msg imsg.Message) string {
	d, ok := m.delivery[msg.GUID]
	if !ok {
		return ""
	}
	switch d.state {
	case "delivered":
		return deliveredStyle.Render("✓✓ delivered")
	case "failed":
		return failedStyle.Render("✗ not delivered")
	case "pending":
		return pendingStyle.Render("⏳ sending")
	}
	return dim.Render("✓ sent")
}

// renderPending draws the placeholders for the open chat.
func (m Model) renderPending(w int) []string {
	var out []string
	for _, p := range m.pending {
		if !(p.chatID != 0 && p.chatID == m.open) && !(p.chatID == 0 && m.open == 0 && p.to == m.openTo) {
			continue
		}
		what := p.text
		if len(p.files) > 0 {
			names := make([]string, 0, len(p.files))
			for _, f := range p.files {
				names = append(names, filepath.Base(f))
			}
			clip := "📎 " + strings.Join(names, ", ")
			if what != "" {
				what += " " + clip
			} else {
				what = clip
			}
		}
		prefix := dim.Render(p.started.Format("15:04")) + " " + meStyle.Render("me") + " "
		var line string
		if p.failed != "" {
			line = prefix + failedStyle.Render("✗ not sent: "+p.failed) + dim.Render(" · "+what)
		} else {
			secs := int(time.Since(p.started).Seconds())
			line = prefix + pendingStyle.Render(fmt.Sprintf("⏳ sending… %ds", secs)) + dim.Render(" · "+what)
		}
		out = append(out, " "+truncate(line, w-1))
	}
	return out
}

type pendingTickMsg struct{}

// schedulePendingTick refreshes the "sending… Ns" counter once a second.
func (m *Model) schedulePendingTick() tea.Cmd {
	if m.pendingTicking {
		return nil
	}
	m.pendingTicking = true
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return pendingTickMsg{} })
}
