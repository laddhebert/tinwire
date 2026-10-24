# Changelog

## Unreleased

- If ffmpeg crashes while compressing (seen with ffmpeg 9.0.2's frame-threaded H.264 decoder), it is
  retried, the last time with a slower crash-safe decoder, before giving up.
- Videos over the 100 MB attachment limit are compressed automatically with ffmpeg (H.264/AAC,
  up to 1080p/30 fps, sized to ~90 MB; NVENC when available, libx264 otherwise) and then
  attached. Progress shows in the status bar; `ctrl+x` cancels. Clips too long to fit at
  watchable quality get a clear message to trim them.
- Error messages are no longer cut off by the key hints in the status bar.
- `enter` while a video is still compressing queues the send; it goes out automatically once the
  video is attached (`ctrl+x` cancels). The attachment line shows the compression progress.
- Clicking the attachment line above the compose box previews the file (e.g. plays the video)
  before sending; the line says "enter sends · click to preview · ctrl+x removes".
- Drops that arrive without bracketed-paste markers (a burst of plain input) are recognized too.
- `--debug-log` also records the shape of pasted/dropped input (paths only, never message text).
- Sent files keep their original name for the recipient (uploads no longer show a random prefix).
- Compressed copies are deleted after sending (or when removed with `ctrl+x`); only files tinwire
  created are ever deleted.

## 0.1.0 (2026-10-23)

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
