// Package imsg wraps the `imsg rpc` JSON-RPC surface (protocol_version 1, imsg >= 0.19).
package imsg

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/laddhebert/tinwire/rpc"
)

// Error codes from docs/rpc.md.
const (
	CodeDeliveryUnknown = -32001 // may have completed or still in flight; never retry
	CodeDBUnavailable   = -32002
	CodeLaneBlocked     = -32004 // earlier send is in flight; restart the child to clear
)

// Time accepts the ISO 8601 variants imsg emits; unparsable values become zero.
type Time struct{ time.Time }

func (t *Time) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil || s == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05"} {
		if v, err := time.Parse(layout, s); err == nil {
			t.Time = v
			return nil
		}
	}
	return nil
}

type Chat struct {
	ID            int64    `json:"id"`
	Name          string   `json:"name"`
	DisplayName   string   `json:"display_name"`
	ContactName   string   `json:"contact_name"`
	Identifier    string   `json:"identifier"`
	GUID          string   `json:"guid"`
	Service       string   `json:"service"`
	LastMessageAt Time     `json:"last_message_at"`
	IsGroup       bool     `json:"is_group"`
	Participants  []string `json:"participants"`
	UnreadCount   int      `json:"unread_count"`
}

type Attachment struct {
	Filename     string `json:"filename"`
	TransferName string `json:"transfer_name"`
	MimeType     string `json:"mime_type"`
	TotalBytes   int64  `json:"total_bytes"`
	UTI          string `json:"uti"`
	IsSticker    bool   `json:"is_sticker"`
	Missing      bool   `json:"missing"`
	OriginalPath string `json:"original_path"`
}

func (a Attachment) Label() string {
	if a.TransferName != "" {
		return a.TransferName
	}
	if i := strings.LastIndex(a.Filename, "/"); i >= 0 {
		return a.Filename[i+1:]
	}
	return a.Filename
}

type Message struct {
	ID            int64        `json:"id"`
	ChatID        int64        `json:"chat_id"`
	GUID          string       `json:"guid"`
	ChatName      string       `json:"chat_name"`
	IsGroup       bool         `json:"is_group"`
	Sender        string       `json:"sender"`
	SenderName    string       `json:"sender_name"`
	IsFromMe      bool         `json:"is_from_me"`
	Text          string       `json:"text"`
	CreatedAt     Time         `json:"created_at"`
	Attachments   []Attachment `json:"attachments"`
	ReplyToGUID   string       `json:"reply_to_guid"`
	EditedAt      Time         `json:"edited_at"`
	UnsentAt      Time         `json:"unsent_at"`
	Type          string       `json:"type"` // "edited" / "unsent" on live mutation updates
	IsReaction    bool         `json:"is_reaction"`
	ReactionType  string       `json:"reaction_type"`
	ReactionEmoji string       `json:"reaction_emoji"`
	IsReactionAdd bool         `json:"is_reaction_add"`
	ReactedToGUID string       `json:"reacted_to_guid"`
}

type Status struct {
	Version         string `json:"version"`
	ProtocolVersion int    `json:"protocol_version"`
	Database        struct {
		Ready bool   `json:"ready"`
		Error string `json:"error"`
	} `json:"database"`
	Methods []string `json:"methods"`
}

func (s Status) Has(method string) bool {
	for _, m := range s.Methods {
		if m == method {
			return true
		}
	}
	return false
}

type SendResult struct {
	OK        bool   `json:"ok"`
	ID        int64  `json:"id"`
	GUID      string `json:"guid"`
	Transport string `json:"transport"`
}

// API issues typed calls with a per-call timeout.
type API struct {
	C       *rpc.Client
	Timeout time.Duration // reads
	SendTO  time.Duration // sends (AppleScript + receipt verification can take a while)
}

func (a *API) call(timeout time.Duration, method string, params, out any) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return a.C.Call(ctx, method, params, out)
}

func (a *API) Status() (Status, error) {
	var s Status
	err := a.call(a.Timeout, "status", nil, &s)
	return s, err
}

func (a *API) Chats(limit int) ([]Chat, error) {
	var r struct{ Chats []Chat }
	err := a.call(a.Timeout, "chats.list", map[string]any{"limit": limit}, &r)
	return r.Chats, err
}

func (a *API) History(chatID int64, limit int) ([]Message, error) {
	return a.HistoryBefore(chatID, limit, time.Time{})
}

// HistoryBefore returns up to limit messages older than before (exclusive);
// a zero time means the newest messages.
func (a *API) HistoryBefore(chatID int64, limit int, before time.Time) ([]Message, error) {
	p := map[string]any{"chat_id": chatID, "limit": limit, "attachments": true}
	if !before.IsZero() {
		p["end"] = before.UTC().Format(time.RFC3339Nano)
	}
	var r struct{ Messages []Message }
	err := a.call(a.Timeout, "messages.history", p, &r)
	return r.Messages, err
}

// Subscribe starts an all-chat watch from the current tail. Messages arrive as
// "message" notifications on the client.
func (a *API) Subscribe() (int64, error) {
	var r struct {
		Subscription int64 `json:"subscription"`
	}
	err := a.call(a.Timeout, "watch.subscribe",
		map[string]any{"attachments": true, "include_reactions": true, "buffer_limit": 1024}, &r)
	return r.Subscription, err
}

// SendChat sends text and/or a file (a path on the Mac) to an existing chat.
// AppleScript transport only: the IMCore bridge needs SIP disabled, which this
// setup deliberately avoids.
func (a *API) SendChat(chatID int64, text, file string) (SendResult, error) {
	p := map[string]any{"chat_id": chatID, "transport": "applescript"}
	return a.send(p, text, file)
}

// SendTo starts or continues a direct conversation with a phone number or email.
func (a *API) SendTo(to, text, file string) (SendResult, error) {
	p := map[string]any{"to": to, "service": "imessage", "transport": "applescript"}
	return a.send(p, text, file)
}

func (a *API) send(p map[string]any, text, file string) (SendResult, error) {
	if text != "" {
		p["text"] = text
	}
	if file != "" {
		p["file"] = file
	}
	var r SendResult
	err := a.call(a.SendTO, "send", p, &r)
	return r, err
}

// Typing shows or clears the typing indicator in a chat. Best-effort: macOS 26
// often refuses it with an entitlement error.
func (a *API) Typing(chatID int64, on bool) error {
	return a.call(a.Timeout, "typing", map[string]any{"chat_id": chatID, "typing": on}, nil)
}

// SendStatus reports the delivery state of an outgoing message:
// "pending", "sent", "delivered" or "failed".
func (a *API) SendStatus(guid string) (string, error) {
	var r struct {
		State string `json:"send_state"`
	}
	err := a.call(a.Timeout, "message.send_status", map[string]any{"guid": guid}, &r)
	return r.State, err
}

// Read marks a chat read in Messages (which sends read receipts if they are
// enabled in Messages' settings). Best-effort on macOS 26.
func (a *API) Read(chatID int64) error {
	return a.call(a.Timeout, "read", map[string]any{"chat_id": chatID}, nil)
}

// WatchNote is the params of a "message" notification.
type WatchNote struct {
	Subscription int64   `json:"subscription"`
	Message      Message `json:"message"`
}

// Overflow is the params of a terminal "watch.overflow" notification.
type Overflow struct {
	Subscription     int64 `json:"subscription"`
	ResumeAfterRowID int64 `json:"resume_after_rowid"`
}

// Code extracts the JSON-RPC error code, or 0.
func Code(err error) int {
	var re *rpc.Error
	if errors.As(err, &re) {
		return re.Code
	}
	return 0
}
