package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/laddhebert/tinwire/imsg"
)

// seenStore remembers, per chat, the newest message time the user has viewed
// in tinwire. With SIP on, imsg cannot mark chats read in Messages, so the
// Mac keeps reporting them unread; this keeps them read here across refreshes
// and restarts. Stored in <state>/seen.json (0600), keyed by chat GUID.
type seenStore struct {
	mu   sync.Mutex
	path string
	At   map[string]time.Time `json:"seen"`
}

func loadSeen(dir string) *seenStore {
	s := &seenStore{At: map[string]time.Time{}}
	if dir == "" {
		return s
	}
	s.path = filepath.Join(dir, "seen.json")
	if b, err := os.ReadFile(s.path); err == nil {
		_ = json.Unmarshal(b, s)
		if s.At == nil {
			s.At = map[string]time.Time{}
		}
	}
	return s
}

func chatKey(c imsg.Chat) string {
	if c.GUID != "" {
		return c.GUID
	}
	return "id:" + strconv.FormatInt(c.ID, 10)
}

// covers reports whether everything in the chat up to its last message has
// been viewed here.
func (s *seenStore) covers(c imsg.Chat) bool {
	if s == nil || c.LastMessageAt.IsZero() {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.At[chatKey(c)]
	return ok && !c.LastMessageAt.After(t)
}

func (s *seenStore) mark(c imsg.Chat, at time.Time) {
	if s == nil || at.IsZero() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := chatKey(c)
	if prev, ok := s.At[k]; ok && !at.After(prev) {
		return
	}
	s.At[k] = at
	if s.path == "" {
		return
	}
	b, err := json.Marshal(s)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(s.path), 0o700)
	tmp := s.path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, s.path)
	}
}

// markSeen records the open chat as viewed up to its newest known message.
func (m *Model) markSeen(id int64) {
	c, ok := m.chatByID(id)
	if !ok {
		return
	}
	at := c.LastMessageAt.Time
	if list := m.msgs[id]; len(list) > 0 && list[len(list)-1].CreatedAt.After(at) {
		at = list[len(list)-1].CreatedAt.Time
	}
	if at.IsZero() {
		at = time.Now()
	}
	m.seen.mark(c, at)
}
