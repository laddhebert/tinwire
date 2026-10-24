# Changelog

## 0.1.0 (unreleased)

First public release. tinwire grew out of a private tool used daily on an Omarchy Linux desktop
against a Mac mini on macOS 26.7.

- Terminal iMessage client: chat list, conversation, compose, live updates, reconnect with
  backoff, `--check`, `--dry-run`.
- Talks to [imsg](https://github.com/openclaw/imsg) 0.19+ (`imsg rpc`) over one ssh session; SIP
  stays on (AppleScript sends only).
- Contact names from the Mac's address book; override file.
- Attachments: click to open (HEIC and CAF converted on the Mac), save, send by drag-and-drop,
  `ctrl+v` screenshot or path; only safe types auto-open.
- Mouse: click chats, messages and the compose box; wheel scrolling; drag to select and copy.
- Unread chats in bold; read state remembered locally.
- Shift+Enter for new lines (CSI-u rewritten for Bubble Tea).
- Desktop notifications, typing indicators (best effort), tapbacks on the latest message.
