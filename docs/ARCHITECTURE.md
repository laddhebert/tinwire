# Architecture

## Why it is built this way

The goal was iMessage on Linux without weakening the Mac. The options considered (October 2026,
macOS 26.7):

| Option | Result |
|---|---|
| BlueBubbles Server 1.9.9, default mode | Sending is broken on macOS 26: chat GUIDs changed to `any;-;…` and its AppleScript send fails with error -1700 (BlueBubbles server issue #777; fix PR #832 unmerged). |
| BlueBubbles Private API | Needs SIP disabled; its helper has open macOS 26 bugs (#776, #779, #814). Rejected. |
| Cloud relays (BlueBubbles' Cloudflare/ngrok, Beeper) | Expose the server to the internet or route messages through a third party. Rejected. |
| **`imsg rpc` over ssh** | `imsg` 0.19 handles macOS 26 chat GUIDs, reads `chat.db` directly and sends through AppleScript. SIP stays on, nothing new listens on the network. **Chosen.** |

## Components

```
main.go          flags, ssh dialer, --check, alias file
keyfix/          terminal input wrapper: rewrites Shift/Ctrl+Enter (CSI u) to alt+enter
rpc/             JSON-RPC 2.0 client over a child process's stdin/stdout
imsg/            typed wrappers for the imsg RPC methods and objects
remote/          one-shot commands on the Mac over ssh: contacts, file transfer, tapbacks
ui/              Bubble Tea model (model.go), rendering (view.go), mouse (mouse.go),
                 drag-and-drop and clipboard (drop.go), text selection (selection.go),
                 debug log (debug.go)
scripts/         pty-smoke.py: drives the real TUI in a pseudo-terminal for end-to-end tests
```

### The session

`tinwire` starts one child process: `ssh -T -o BatchMode=yes … <mac> -- '/usr/local/bin/imsg' rpc`
(ServerAlive every 15 s, ConnectTimeout 10 s). `imsg rpc` speaks newline-delimited JSON-RPC 2.0 on
stdin/stdout. On connect the client calls `status` (must report the database ready), then
`watch.subscribe` for all chats (attachments and reactions included), then `chats.list`.

`rpc.Client` multiplexes calls by id. Notifications (`message`, `watch.overflow`) go into an
unbounded queue drained by a separate goroutine, so a slow UI can never stall the responses to
unrelated calls. Every call has a timeout (20 s reads, 90 s sends). Closing the client closes
stdin; `imsg rpc` drains and exits; the process is killed after 3 s if it has not.

If the session drops, the UI reconnects with exponential backoff (2 s doubling to 1 min) and then
reloads the chat list and the open conversation, so nothing received meanwhile is missed. A
`watch.overflow` notification is handled the same way.

### RPC methods used

| Method | Params used | Notes |
|---|---|---|
| `status` | none | readiness; `methods` lists what is usable |
| `chats.list` | `limit` | |
| `messages.history` | `chat_id`, `limit`, `attachments`, `end` | `end` (exclusive, ISO 8601) pages back in time |
| `watch.subscribe` | `attachments`, `include_reactions`, `buffer_limit` | all chats, from the current tail |
| `send` | `chat_id` or `to`+`service`, `text`, `file`, `transport: "applescript"` | one file per call |
| `typing` | `chat_id`, `typing` (bool) | not documented in imsg's rpc.md; found by probing a nonexistent chat id |
| `read` | `chat_id` | bridge-only in imsg 0.19: refused with SIP on, so it is tried once per session and then left off |

`imsg rpc` rejects unknown parameters with `-32602`, so parameter names matter.

### One-shot ssh commands (`remote/`)

Run as `ssh … <mac> -- /bin/sh -c '<script>'` with every argument single-quoted.

- **Contacts.** `imsg` cannot use the Contacts framework from an ssh session
  (`contacts.available=false`), so the client reads
  `~/Library/Application Support/AddressBook/{,Sources/*/}AddressBook-v22.abcddb` with
  `/usr/bin/sqlite3 -readonly -json "file:…?immutable=1"` (phones and emails joined to names).
  Keys are lowercased emails and phone digits, plus the last 10 digits.
- **Downloads.** `cat` of the attachment's `original_path`, which must be under
  `/Users/*/Library/Messages/Attachments/` with no `..`. HEIC/HEIF are converted with `sips` to
  JPEG and CAF with `afconvert` to M4A on the Mac first. Files land in
  `~/.cache/tinwire/attachments/<message guid>/` (0700 dirs, 0600 files) and are reused.
- **Uploads.** The local file is streamed into `~/Library/Caches/tinwire/outbox/<random>-<name>`
  (umask 077), passed to `send` as `file`, and deleted afterwards. `imsg` stages its own copy under
  `~/Library/Messages/Attachments/imsg/` before Messages reads it.
- **Tapbacks.** `imsg react --chat-id N --reaction R`, which uses Messages' "react to last message"
  shortcut through System Events. Needs Accessibility; reports a clear error without it.

## Security model

- Transport is ssh with key auth only (`BatchMode=yes`). No port is opened on either machine.
- The Mac-side `imsg` is the official notarized build; nothing is injected into Messages.app.
- Sender-controlled data is treated as hostile:
  - filenames are reduced to a plain basename (`remote.SafeName`): no path separators, control
    characters, leading dots or dashes;
  - only images, video, audio, PDF and plain text are handed to `xdg-open`; any other type (for
    example a `.desktop` file or a script) is saved, never opened;
  - remote paths are checked against the Messages attachment store before reading, and the outbox
    path before deleting.
- `--debug-log` records layout and input events only, never message text or handles.
- `--check` prints counts only, so it is safe in logs.

## Sending semantics

`imsg` reports delivery uncertainty explicitly and the client respects it:

- `-32001` (delivery unknown): the message may have gone out. Never retried automatically.
- A failure with `retry_safe: true`, or an upload that failed before anything was sent, puts the
  text and unsent attachments back in the compose box.
- `-32004` (send lane blocked by an earlier in-flight send): `ctrl+r` restarts the `imsg rpc` child,
  which is the only way to clear it.
- With several attachments, the text rides with the first file; the rest follow one per call. If
  one fails, the status bar says how many went out.

## UI notes

- State lives in one Bubble Tea `Model`; `Update` is a thin wrapper that also keeps the chat
  list's scroll offset in the model (`syncListTop`) so mouse clicks map to the rows on screen.
- Unread counts come from `chats.list` and are updated live from the watch. Because the Mac
  cannot be told a chat was read (bridge-only), tinwire keeps its own read state:
  `seen.json` maps chat GUID to the newest message time viewed here; a chat whose
  `last_message_at` is not newer is shown read. Written atomically, mode 0600.
- Attachment lines are found after rendering (`findAttachLines`): a line whose body starts with
  `📎 ` belongs to the message at that line; quoted clips (`“📎`) are excluded. A click without a
  drag on such a line opens exactly that attachment.
- Typing: the indicator starts on the first keystroke that changes the compose text, refreshes
  while typing, and stops 5 s after the last keystroke, on send, or when leaving the chat. The
  first refusal from Messages turns the feature off for the session (same for `read`).
- Terminal focus (`tea.WithReportFocus`) decides whether an incoming message in the open chat
  counts as seen (no notification, marked read) or not.
- Selection: mouse capture disables the terminal's own selection (except with shift), so
  `selection.go` implements it for the conversation. Press records an anchor in content
  coordinates (line in the full conversation, cell column), motion with the button held extends it
  and scrolls at the edges, release copies `ansi.Strip`ped text via wl-copy/xclip/xsel. The cursor
  bar column is never copied. Highlighted lines are drawn uncoloured in reverse video.
- Modified Enter: foot (as configured by Omarchy), kitty and Ghostty send Shift/Ctrl+Enter as CSI u
  (`ESC[13;2u`), xterm as `ESC[27;2;13~`. Bubble Tea v1 does not parse these and reports an
  unknown-sequence message whose bytes alias its input buffer; by the time `Update` runs, the next
  keystroke has often overwritten them (verified), so decoding there is unreliable. `keyfix.Wrap`
  wraps stdin as a `term.File` (same Fd, so raw mode and the cancelable reader still work) and
  rewrites those sequences to `ESC CR`, which Bubble Tea parses as alt+enter, the compose box's
  new-line key.
- Drops: terminals deliver a dropped file as pasted text. foot sends newline-separated `file://`
  URIs, Alacritty raw paths, Ghostty shell-escaped paths. A paste becomes attachments only if every
  item is an existing regular file; otherwise it is inserted as text.

## Testing

```
make test        # go vet + go test -race ./...
tinwire --check  # the whole chain: ssh, imsg, database, history paging, contacts, watch
make smoke       # scripts/pty-smoke.py: real binary, real data, --dry-run, stub wl-copy
```

Unit tests cover the RPC client (against a re-executed fake server), rendering at several terminal
sizes, unread/bold handling, the message cursor and history paging, mouse mapping (including a
scrolled list), drop parsing for each terminal's format, the attachment path guard, filename
sanitizing, shell quoting, the outbox upload/remove cycle, and the dry-run/cap/disconnected send
guards.

**Pseudo-terminal testing gotcha.** On startup the TUI asks the terminal for its background colour
(`ESC]11;?`) and cursor position (`ESC[6n`) and waits for the answers. Real terminals answer at
once. A test harness that does not answer stalls the program, and the first input bytes (often a
mouse click) are swallowed as the "answer". `scripts/pty-smoke.py` replies like a terminal. Piping
input through `script(1)` also splits escape sequences across reads; do not use it for mouse tests.
