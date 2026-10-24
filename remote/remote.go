// Package remote runs one-shot commands on the Mac over ssh (or locally when
// Host is "local"): contacts lookup, attachment transfer, and UI tapbacks.
// The long-lived JSON-RPC session lives in package rpc.
package remote

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// SSHOptions are shared with the RPC session so both behave the same.
var SSHOptions = []string{
	"-o", "BatchMode=yes",
	"-o", "ConnectTimeout=10",
	"-o", "ServerAliveInterval=15",
	"-o", "ServerAliveCountMax=3",
}

type Mac struct {
	Host string // ssh destination, or "local"
	Imsg string // path to imsg on the Mac
}

// Command returns an exec.Cmd running script with /bin/sh on the Mac.
func (m Mac) Command(ctx context.Context, script string) *exec.Cmd {
	if m.Host == "local" {
		return exec.CommandContext(ctx, "/bin/sh", "-c", script)
	}
	args := append([]string{"-T"}, SSHOptions...)
	args = append(args, m.Host, "--", "/bin/sh -c "+Quote(script))
	return exec.CommandContext(ctx, "ssh", args...)
}

func (m Mac) run(ctx context.Context, script string, stdin io.Reader, stdout io.Writer) error {
	cmd := m.Command(ctx, script)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if s := strings.TrimSpace(stderr.String()); s != "" {
			return fmt.Errorf("%w: %s", err, lastLine(s))
		}
		return err
	}
	return nil
}

// ---- contacts ----

const contactsQuery = `SELECT 'p' AS k, p.ZFULLNUMBER AS v, r.ZFIRSTNAME AS f, r.ZLASTNAME AS l, r.ZORGANIZATION AS o, r.ZNICKNAME AS n
FROM ZABCDPHONENUMBER p JOIN ZABCDRECORD r ON p.ZOWNER = r.Z_PK
UNION ALL
SELECT 'e', e.ZADDRESS, r.ZFIRSTNAME, r.ZLASTNAME, r.ZORGANIZATION, r.ZNICKNAME
FROM ZABCDEMAILADDRESS e JOIN ZABCDRECORD r ON e.ZOWNER = r.Z_PK;`

// Contacts reads every AddressBook source database on the Mac (read-only,
// immutable snapshot) and returns normalized handle -> display name.
// imsg cannot use the Contacts framework from an ssh session, but the
// AddressBook SQLite files are readable with the Full Disk Access sshd has.
func (m Mac) Contacts(ctx context.Context) (map[string]string, error) {
	script := `cd "$HOME/Library/Application Support/AddressBook" 2>/dev/null || exit 0
for db in AddressBook-v22.abcddb Sources/*/AddressBook-v22.abcddb; do
  [ -f "$db" ] || continue
  /usr/bin/sqlite3 -readonly -json "file:$db?immutable=1" ` + Quote(contactsQuery) + ` || exit 1
  echo '@@END'
done`
	var out bytes.Buffer
	if err := m.run(ctx, script, nil, &out); err != nil {
		return nil, fmt.Errorf("contacts: %w", err)
	}
	type row struct{ K, V, F, L, O, N *string }
	names := map[string]string{}
	for _, chunk := range strings.Split(out.String(), "@@END") {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		var rows []row
		if err := json.Unmarshal([]byte(chunk), &rows); err != nil {
			return nil, fmt.Errorf("contacts: parse: %w", err)
		}
		for _, r := range rows {
			if r.K == nil || r.V == nil {
				continue
			}
			name := strings.TrimSpace(deref(r.F) + " " + deref(r.L))
			if name == "" {
				name = strings.TrimSpace(deref(r.N))
			}
			if name == "" {
				name = strings.TrimSpace(deref(r.O))
			}
			if name == "" {
				continue
			}
			for _, k := range HandleKeys(*r.V) {
				if _, taken := names[k]; !taken {
					names[k] = name
				}
			}
		}
	}
	return names, nil
}

// HandleKeys returns lookup keys for a phone number or email: emails are
// lowercased, phone numbers reduce to digits plus the last 10 digits so
// "+1 (555) 123-4567" and "5551234567" meet.
func HandleKeys(h string) []string {
	h = strings.TrimSpace(h)
	if strings.Contains(h, "@") {
		return []string{strings.ToLower(h)}
	}
	var d strings.Builder
	for _, r := range h {
		if r >= '0' && r <= '9' {
			d.WriteRune(r)
		}
	}
	digits := d.String()
	if len(digits) < 5 {
		return nil
	}
	keys := []string{digits}
	if len(digits) > 10 {
		keys = append(keys, digits[len(digits)-10:])
	}
	return keys
}

// ---- attachments ----

// attachmentPath only allows files inside Messages' attachment store, so a
// path coming back from chat.db can never be used to read anything else.
var attachmentPath = regexp.MustCompile(`^/Users/[^/]+/Library/Messages/Attachments/`)

func checkAttachmentPath(p string) error {
	if !attachmentPath.MatchString(p) || strings.Contains(p, "/../") || strings.HasSuffix(p, "/..") {
		return fmt.Errorf("refusing attachment path outside ~/Library/Messages/Attachments: %q", p)
	}
	return nil
}

// Download copies a Messages attachment to local path dst. HEIC/HEIF images
// are converted to JPEG and CAF audio to M4A on the Mac (built-in sips and
// afconvert) so they open on Linux; it returns the extension actually written.
func (m Mac) Download(ctx context.Context, src, dst string) (ext string, err error) {
	if err := checkAttachmentPath(src); err != nil {
		return "", err
	}
	q := Quote(src)
	var script string
	switch strings.ToLower(path.Ext(src)) {
	case ".heic", ".heif":
		ext = ".jpg"
		script = `t=$(mktemp -d) && trap 'rm -rf "$t"' EXIT && /usr/bin/sips -s format jpeg ` + q + ` --out "$t/x.jpg" >/dev/null && cat "$t/x.jpg"`
	case ".caf":
		ext = ".m4a"
		script = `t=$(mktemp -d) && trap 'rm -rf "$t"' EXIT && /usr/bin/afconvert -f m4af -d aac ` + q + ` "$t/x.m4a" && cat "$t/x.m4a"`
	default:
		ext = path.Ext(src)
		script = `cat -- ` + q
	}
	if ext != path.Ext(src) {
		dst = strings.TrimSuffix(dst, filepath.Ext(dst)) + ext
	}
	f, err := os.OpenFile(dst+".part", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	if err := m.run(ctx, script, nil, f); err != nil {
		f.Close()
		os.Remove(dst + ".part")
		return "", fmt.Errorf("download: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return ext, os.Rename(dst+".part", dst)
}

const outbox = `$HOME/Library/Caches/tinwire/outbox`

// Upload copies a local file into a private outbox on the Mac and returns its
// absolute path there. imsg then stages its own copy for Messages.
func (m Mac) Upload(ctx context.Context, local string) (string, error) {
	f, err := os.Open(local)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", err
	}
	name := hex.EncodeToString(rnd[:]) + "-" + SafeName(filepath.Base(local))
	script := `umask 077 && d="` + outbox + `" && mkdir -p "$d" && cat > "$d/"` + Quote(name) + ` && printf '%s' "$d/"` + Quote(name)
	var out bytes.Buffer
	if err := m.run(ctx, script, f, &out); err != nil {
		return "", fmt.Errorf("upload: %w", err)
	}
	return out.String(), nil
}

// RemoveUpload deletes a file previously returned by Upload.
func (m Mac) RemoveUpload(ctx context.Context, remote string) error {
	if !strings.Contains(remote, "/Library/Caches/tinwire/outbox/") || strings.Contains(remote, "..") {
		return fmt.Errorf("refusing to remove %q", remote)
	}
	return m.run(ctx, `rm -f -- `+Quote(remote), nil, io.Discard)
}

// ---- tapbacks ----

// ErrAccessibility means System Events automation is not allowed for the
// process running imsg (sshd on the Mac).
var ErrAccessibility = errors.New("tapbacks need Accessibility permission for ssh sessions on the Mac (System Settings > Privacy & Security > Accessibility)")

// React sends a standard tapback to the LAST message of a chat through
// Messages UI automation (`imsg react`). It needs a logged-in, unlocked
// desktop session and Accessibility permission; no SIP change.
func (m Mac) React(ctx context.Context, chatID int64, reaction string) error {
	switch reaction {
	case "love", "like", "dislike", "laugh", "emphasis", "question":
	default:
		return fmt.Errorf("unknown reaction %q", reaction)
	}
	script := fmt.Sprintf("%s react --chat-id %d --reaction %s --json", Quote(m.Imsg), chatID, reaction)
	err := m.run(ctx, script, nil, io.Discard)
	if err == nil {
		return nil
	}
	if s := strings.ToLower(err.Error()); strings.Contains(s, "assistive") || strings.Contains(s, "accessibility") {
		return ErrAccessibility
	}
	return err
}

// ---- helpers ----

// SafeName turns a sender-controlled filename into a plain local basename.
func SafeName(s string) string {
	s = filepath.Base(strings.ReplaceAll(s, "\\", "/"))
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '/' || r == ':' {
			return '_'
		}
		return r
	}, s)
	s = strings.TrimLeft(strings.TrimSpace(s), ".-")
	if s == "" {
		s = "attachment"
	}
	if len(s) > 120 {
		ext := filepath.Ext(s)
		if len(ext) > 12 {
			ext = ""
		}
		s = s[:120-len(ext)] + ext
	}
	return s
}

// Quote single-quotes s for /bin/sh.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}
