#!/usr/bin/env python3
"""End-to-end smoke test: drive the real tinwire in a pseudo-terminal.

Runs tinwire --dry-run (nothing is ever sent) against the real Mac and checks
that clicks, drag-to-select + copy, and a foot-style file drop work. Prints
only pass/fail and lengths, never message content.

Usage: pty-smoke.py [path-to-tinwire]     (default: tinwire on PATH)

Two gotchas this harness handles (see docs/ARCHITECTURE.md, Testing):
  * The TUI queries the terminal at startup (ESC]11;? background colour,
    ESC[6n cursor position) and waits. A harness must answer like a real
    terminal or the first input bytes are swallowed as the answer.
  * Each escape sequence must be written in one piece; piping through
    script(1) splits them across reads.
"""
import os, pty, select, shutil, struct, sys, tempfile, time, fcntl, termios

binary = sys.argv[1] if len(sys.argv) > 1 else shutil.which("tinwire")
if not binary:
    sys.exit("tinwire not found")

work = tempfile.mkdtemp(prefix="imsg-smoke-")
fakebin = os.path.join(work, "bin")
os.mkdir(fakebin)
copied = os.path.join(work, "copied.txt")
# A stand-in wl-copy so the test never touches the real clipboard.
with open(os.path.join(fakebin, "wl-copy"), "w") as f:
    f.write("#!/bin/sh\ncat > '%s'\n" % copied)
os.chmod(os.path.join(fakebin, "wl-copy"), 0o755)
img = os.path.join(work, "drop test.png")
with open(img, "wb") as f:
    f.write(b"\x89PNG\r\n\x1a\n" + b"\0" * 64)

COLS, ROWS = 140, 40
pid, fd = pty.fork()
if pid == 0:
    env = dict(os.environ, TERM="xterm-256color", PATH=fakebin + ":" + os.environ.get("PATH", ""))
    env.pop("WAYLAND_DISPLAY", None)
    os.execve(binary, ["tinwire", "--dry-run", "--no-notify", "--cache-dir", os.path.join(work, "cache")], env)
fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", ROWS, COLS, 0, 0))

buf = bytearray()
def pump(sec):
    end = time.time() + sec
    while time.time() < end:
        r, _, _ = select.select([fd], [], [], 0.05)
        if not r:
            continue
        try:
            data = os.read(fd, 65536)
        except OSError:
            return
        buf.extend(data)
        if b"\x1b]11;?" in data:
            os.write(fd, b"\x1b]11;rgb:1e1e/1e1e/1e1e\x1b\\")
        if b"\x1b[6n" in data:
            os.write(fd, b"\x1b[1;1R")

def take():
    global buf
    out = bytes(buf); buf = bytearray(); return out

def mouse(code, x, y, release=False):
    os.write(fd, b"\x1b[<%d;%d;%d%s" % (code, x, y, b"m" if release else b"M"))

CONV, COMPOSE = "o open · s save".encode(), "↵ send".encode()
results = {}

pump(6); take()
mouse(0, 5, 1); mouse(0, 5, 1, True); pump(4)
results["click chat opens it (compose focus)"] = COMPOSE in take()
mouse(0, 80, 20); mouse(0, 80, 20, True); pump(1.5)
results["click message selects it (conversation focus)"] = CONV in take()
# Drag across two rows of the conversation, then release.
mouse(0, 45, 18); pump(0.2)
for x in range(46, 130, 6):
    mouse(32, x, 18); time.sleep(0.01)
mouse(32, 130, 19); pump(0.3)
mouse(0, 130, 19, True); pump(1.5)
out = take()
n = os.path.getsize(copied) if os.path.exists(copied) else 0
results["drag selects and copies text"] = n > 0 and b"copied to clipboard" in out
mouse(0, 80, 38); mouse(0, 80, 38, True); pump(1.5)
results["click compose focuses it"] = COMPOSE in take()
uri = "file://" + img.replace(" ", "%20")
os.write(fd, b"\x1b[200~" + uri.encode() + b"\r\n\x1b[201~"); pump(1.5)
results["dropped file shows as attachment"] = b"drop test.png" in take()
os.write(fd, b"\r"); pump(1.5)
results["dry-run send includes the file"] = b"would send" in take()
# Shift+Enter as foot sends it (ESC[13;2u), with typing right behind it.
os.write(fd, b"a"); os.write(fd, b"\x1b[13;2u"); os.write(fd, b"b"); pump(0.5)
os.write(fd, b"\r"); pump(1.5)
results["shift+enter makes a new line (a, newline, b = 3 chars)"] = b"would send 3 chars" in take()
os.write(fd, b"\x1b"); pump(0.5); os.write(fd, b"q"); pump(3)
_, status = os.waitpid(pid, 0)
results["clean exit"] = os.waitstatus_to_exitcode(status) == 0
shutil.rmtree(work, ignore_errors=True)

for name, ok in results.items():
    print(("PASS " if ok else "FAIL ") + name)
print("copied %d bytes (content not shown)" % n)
sys.exit(0 if all(results.values()) else 1)
