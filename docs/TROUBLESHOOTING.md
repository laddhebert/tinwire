# Troubleshooting

Start with the end-to-end check. It prints counts only, never message content:

```
tinwire --check
```

Expected output ends in `OK`. Each line tests one link: ssh and `imsg` (version line), the
Messages database, history paging, contacts, and the live watch.

## Status-bar messages

| Message | Meaning | What to do |
|---|---|---|
| `connecting…` / `retrying in Ns` | ssh to the Mac failed or dropped; it retries with backoff up to 1 min | Check the Mac is up and `ssh <your host> true` works without a password prompt. `ctrl+r` retries immediately. |
| `Messages database not readable on the Mac (Full Disk Access?)` | `imsg` cannot open `chat.db` from the ssh session | On the Mac: System Settings → Privacy & Security → Full Disk Access → add `/usr/libexec/sshd-keygen-wrapper`. |
| `delivery unknown: may have been sent, NOT retried` | `imsg` could not confirm the send (`-32001`) | Look at the thread (on your phone or the Mac) before sending again. |
| `send lane blocked …` | An earlier send is still in flight inside `imsg` (`-32004`) | `ctrl+r` restarts `imsg rpc`, which clears it. |
| `send failed: …` with your text back in the box | The send provably never started | Fix the cause and press `enter` again. |
| `typing indicators off for this session` | Messages refused the typing call (common on macOS 26) | Nothing; restart to try again. `--no-typing` silences it. |
| `mark-as-read off for this session` | Messages refused the read call | Same as above; `--no-mark-read`. |
| `contact names unavailable: …` | Reading the AddressBook over ssh failed | Names fall back to numbers. Check Full Disk Access; `~/.config/tinwire/contacts` still works. |
| `tapback: tapbacks need Accessibility permission …` | `imsg react` cannot drive System Events | Granting Accessibility to ssh sessions would let any ssh login control the Mac desktop; react from your phone instead, or grant it knowingly. |
| `tapbacks can only go on the latest message …` | Messages' shortcut only reacts to the newest message | Use your phone for older messages. |
| `… is not on the Mac (not downloaded from iCloud, or pruned)` | The attachment file is missing on the Mac | Open the conversation in Messages on the Mac once so it downloads. |
| `(.xyz files are not opened automatically)` | The file type is not on the safe list | It was saved to `~/Downloads`; open it yourself if you trust it. |
| `attach: … over the 100 MB limit` | File too large (not a video) | Send a smaller file or a link. Videos are compressed automatically when `ffmpeg` is installed. |
| `compressing <video>… N%` | A video over the limit is being shrunk to fit | Wait for "attached", then press enter. `ctrl+x` cancels. |
| `… cannot fit in … at watchable quality; trim it first` | The clip is too long for the limit even when compressed | Trim it, or send a link. |
| `open a chat first, then drop or paste the file` | Dropped a file with no chat open | Open the chat, then drop again. |
| `ctrl+v needs wl-paste (wl-clipboard)` | No Wayland clipboard tool | install `wl-clipboard` (Wayland) or `xclip` (X11). |
| `beginning of conversation` | No older history | |

## Other symptoms

**First send seems to hang, or fails with an AppleScript/Automation error.** macOS is asking
whether ssh sessions may control Messages. Someone has to click Allow on the Mac once. After that it is
remembered.

**Chats stay unread on the Mac and iPhone after reading them here.** Expected with SIP on: imsg's
`read`/`chat-mark` need the IMCore bridge, which refuses to run with SIP enabled. tinwire marks
them read for itself. Read them on the phone or Mac to clear the badges there.

**Clicking a picture does nothing visible.** Check the status bar: `opened …` means your viewer
was started (it may have opened on another workspace); an error names the cause. The default
viewer comes from `xdg-mime query default image/jpeg`.

**Names show as numbers.** Either the number is not in your contacts (the Mac shows a number too),
it is an SMS short code, or contacts failed to load (see above). Add it to
`~/.config/tinwire/contacts`.

**Can't select or copy text.** Drag over the conversation: the selection is highlighted and copied
to the clipboard on release (status: `copied to clipboard`). Outside the conversation, or for the
terminal's own selection, hold **shift** while dragging, then `ctrl+shift+c` (foot). If copying
fails, the status bar names the missing tool: install `wl-clipboard`. `--no-mouse` hands the mouse
back to the terminal completely.

**Key hints missing from the status bar.** On narrow windows a compact hint is shown, and below
about 50 columns none; widen the window.

**Shift+Enter sends instead of making a new line.** You are on a version before 0.5.1, or your
terminal sends something other than CSI u / modifyOtherKeys for it. `alt+enter` and `ctrl+j` always
work.

**Clicks do nothing.** Your terminal may not send mouse events in this mode, or `--no-mouse` is
set. Run with `--debug-log /tmp/tinwire.log` and click: each click is logged with its position and
the pane it landed in. The log never contains message content.

**Dropping a file inserts its path as text.** The paste was not entirely existing files (for
example a file that has since moved, or a directory). Directories are not sent.

## Logs and versions

- `tinwire --version`; on the Mac `imsg --version` (needs 0.19 or newer).
- `--debug-log FILE` for UI events.
- `imsg` diagnostics go to its stderr; the last lines are included in disconnect errors.
