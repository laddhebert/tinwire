# Security

## Reporting

Please report vulnerabilities privately through GitHub's "Report a vulnerability" (Security tab)
rather than a public issue.

## Threat model

tinwire connects a Linux machine to a Mac that is signed in to your Apple ID. Anyone who can run
tinwire with your ssh key can read and send your messages, so treat that key like your phone.

What tinwire does to limit exposure:

- **No network surface.** It opens no ports and runs no daemon. It uses ssh with key auth only
  (`BatchMode=yes`) and an existing `imsg` binary on the Mac.
- **No privilege escalation.** It does not need, and never uses, imsg's IMCore bridge (which
  requires disabling System Integrity Protection). It does not ask for Accessibility permission.
- **Sender-controlled input is untrusted.** Attachment paths from the Messages database must be
  inside `~/Library/Messages/Attachments/` before anything is read; file names are reduced to a
  plain base name; only images, video, audio, PDF and plain text are passed to `xdg-open`, so a
  received `.desktop` file or script is never executed.
- **Shell safety.** Every value passed to a remote shell is single-quoted; the tests run the
  quoting through a real shell.
- **Private files.** Downloads are cached with mode 0700/0600; uploads go to a 0600 outbox on the
  Mac and are deleted after sending; local read state is 0600.
- **No content in diagnostics.** `--check` and `--debug-log` print counts and UI events only.

What it relies on: your ssh setup and the Mac's own protections. Full Disk Access for ssh
sessions (needed to read `chat.db`) lets any ssh login read all files of that user on the Mac.
