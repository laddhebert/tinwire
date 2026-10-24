package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// attachRef identifies one attachment drawn in the conversation.
type attachRef struct{ msg, att int }

// findAttachLines maps each content line that shows an attachment ("📎 …"
// as the start of a message body line) to its message and attachment index.
// Quoted context such as ↳ “📎 photo.jpg” or reacted ♥ to “📎 …” has a quote
// mark before the clip and is not counted.
func (m Model) findAttachLines() map[int]attachRef {
	out := map[int]attachRef{}
	if len(m.lineStarts) == 0 {
		return out
	}
	perMsg := map[int]int{}
	for i, line := range m.vpLines {
		plain := ansi.Strip(line)
		idx := strings.Index(plain, "📎 ")
		if idx <= 0 || plain[idx-1] != ' ' {
			continue
		}
		mi := m.messageAtLine(i)
		if mi < 0 || mi >= len(m.msgs[m.open]) || len(m.msgs[m.open][mi].Attachments) == 0 {
			continue
		}
		ai := perMsg[mi]
		if ai >= len(m.msgs[m.open][mi].Attachments) {
			continue
		}
		perMsg[mi] = ai + 1
		out[i] = attachRef{msg: mi, att: ai}
	}
	return out
}
