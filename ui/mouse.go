package ui

import tea "github.com/charmbracelet/bubbletea"

// handleMouse maps clicks and the wheel onto the three panes:
//   - chat list: click opens that chat (focus goes to its compose box);
//     wheel moves the selection
//   - conversation: click selects that message (message actions apply);
//     press and drag selects text, release copies it (see selection.go);
//     wheel scrolls, and reaching the top loads older history
//   - compose box: click focuses it
//
// Prompts (new conversation, attach, reaction picker) ignore the mouse.
func (m Model) handleMouse(e tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.w == 0 || m.focus == focusTo || m.focus == focusAttach || m.focus == focusReact {
		debugf("mouse ignored: w=%d focus=%d", m.w, m.focus)
		return m, nil
	}
	listW, _, bodyH, vpH := m.dims()
	debugf("mouse x=%d y=%d action=%v button=%v | w=%d h=%d listW=%d bodyH=%d vpH=%d focus=%d open=%d yoff=%d lines=%d starts=%d chats=%d connected=%v listTop=%d",
		e.X, e.Y, e.Action, e.Button, m.w, m.h, listW, bodyH, vpH, m.focus, m.open, m.vp.YOffset, m.vp.TotalLineCount(), len(m.lineStarts), len(m.chats), m.connected, m.listTop)
	if e.Y >= bodyH { // status bar
		return m, nil
	}
	inList := e.X < listW
	inConv := e.X > listW // listW itself is the separator column
	vpTop := 1            // header line
	composeTop := vpTop + vpH
	convX0 := listW + 1

	// Drag-to-select in the conversation: motion with the button held
	// extends the selection (scrolling at the edges); release copies it.
	if m.selecting && e.Action == tea.MouseActionMotion {
		switch {
		case e.Y < vpTop:
			m.vp.ScrollUp(1)
		case e.Y >= composeTop:
			m.vp.ScrollDown(1)
		}
		y := min(max(e.Y, vpTop), composeTop-1)
		p := m.contentPos(e.X, y, convX0, vpTop)
		if p != m.selA {
			m.dragged, m.hasSel = true, true
		}
		m.selB = p
		return m, nil
	}
	if m.selecting && e.Action == tea.MouseActionRelease {
		m.selecting = false
		if !m.dragged {
			// A click (no drag) on a 📎 line opens that attachment.
			if ref, ok := m.attachLines[m.pressLine]; ok && ref.msg < len(m.msgs[m.open]) {
				return m, m.openAttachmentsAt(m.msgs[m.open][ref.msg], []int{ref.att}, true)
			}
			return m, nil
		}
		text := m.selectedText()
		if text == "" {
			m.hasSel = false
			return m, nil
		}
		return m, copyText(text)
	}

	if e.Action == tea.MouseActionPress {
		switch e.Button {
		case tea.MouseButtonWheelUp:
			if inList {
				if m.sel > 0 {
					m.sel--
				}
				return m, nil
			}
			if inConv {
				m.vp.ScrollUp(3)
				if m.vp.AtTop() && m.open != 0 {
					return m, m.loadOlder(m.open)
				}
			}
			return m, nil
		case tea.MouseButtonWheelDown:
			if inList {
				if m.sel < len(m.chats)-1 {
					m.sel++
				}
				return m, nil
			}
			if inConv {
				m.vp.ScrollDown(3)
			}
			return m, nil
		}
	}
	if e.Action != tea.MouseActionPress || e.Button != tea.MouseButtonLeft {
		return m, nil
	}
	m.hasSel = false // any click clears an old selection

	switch {
	case inList:
		row := m.listTop + e.Y
		if row < 0 || row >= len(m.chats) {
			return m, nil
		}
		m.sel = row
		if m.chats[row].ID == m.open && m.focus == focusInput {
			return m, nil
		}
		return m, m.openChat(m.chats[row].ID)

	case inConv:
		switch {
		case e.Y >= vpTop && e.Y < composeTop:
			if m.open == 0 {
				return m, nil
			}
			// Start a potential drag selection; a plain click still selects
			// the message.
			m.selecting, m.dragged = true, false
			m.selA = m.contentPos(e.X, e.Y, convX0, vpTop)
			m.selB = m.selA
			m.pressLine = m.vp.YOffset + e.Y - vpTop
			i := m.messageAtLine(m.vp.YOffset + e.Y - vpTop)
			debugf("  conversation click -> message index %d", i)
			if i < 0 {
				return m, nil
			}
			cmd := m.stopTyping()
			m.focus = focusConv
			m.ta.Blur()
			m.cursor = i
			m.refreshVP(false)
			return m, cmd
		case e.Y == composeTop && len(m.attach) > 0:
			// The attachment line above the compose box: preview what will be sent.
			return m, m.previewLocal(m.attach)
		case e.Y >= composeTop:
			if m.open == 0 && m.openTo == "" {
				return m, nil
			}
			m.focus = focusInput
			m.refreshVP(false)
			return m, m.ta.Focus()
		}
	}
	return m, nil
}

// messageAtLine returns the index of the message drawn on content line n of
// the conversation, using the line offsets from the last render.
func (m Model) messageAtLine(n int) int {
	if len(m.lineStarts) == 0 || n < m.lineStarts[0] {
		if len(m.lineStarts) > 0 && n >= 0 {
			return 0 // a date separator above the first message
		}
		return -1
	}
	i := 0
	for j, start := range m.lineStarts {
		if start > n {
			break
		}
		i = j
	}
	return i
}
