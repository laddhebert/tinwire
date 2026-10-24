package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestVideoBitrate(t *testing.T) {
	// The real case: a 6m14s screen recording, 100 MB limit, 90% target.
	v, err := videoBitrate(90_000_000, 374*time.Second)
	if err != nil || v < 1500 || v > 2000 {
		t.Fatalf("6 min clip: %d kbps, %v", v, err)
	}
	if _, err := videoBitrate(90_000_000, 3*time.Hour); err == nil || !strings.Contains(err.Error(), "trim") {
		t.Fatalf("3 h clip should be refused, got %v", err)
	}
	if _, err := videoBitrate(90_000_000, 0); err == nil {
		t.Fatal("zero duration accepted")
	}
}

func TestIsVideoAndArgs(t *testing.T) {
	for _, p := range []string{"a.mp4", "b.MOV", "c.mkv", "d.webm"} {
		if !isVideo(p) {
			t.Errorf("%s not detected", p)
		}
	}
	if isVideo("e.jpg") || isVideo("f") {
		t.Error("non-video detected")
	}
	if a := strings.Join(ffmpegArgs("libx264", "in.mov", "out.mp4", 1800, false), " "); strings.Contains(a, "thread_type") {
		t.Errorf("fast path must use the default (frame-threaded) decoder: %s", a)
	}
	if a := strings.Join(ffmpegArgs("libx264", "in.mov", "out.mp4", 1800, true), " "); !strings.Contains(a, "-thread_type slice -i in.mov") {
		t.Errorf("safe decode not applied: %s", a)
	}
	a := strings.Join(ffmpegArgs("libx264", "in.mov", "out.mp4", 1800, false), " ")
	for _, want := range []string{"-c:v libx264", "-b:v 1800k", "-maxrate 1800k", "-preset veryfast", "-fpsmax 30", "-c:a aac", "+faststart", "-progress pipe:1", "0:a:0?"} {
		if !strings.Contains(a, want) {
			t.Errorf("args missing %q: %s", want, a)
		}
	}
}

// fakeTools puts stand-in ffprobe/ffmpeg on PATH. ffmpeg fails for NVENC (to
// exercise the fallback) and for libx264 writes an output of outBytes after
// printing progress.
func fakeTools(t *testing.T, outBytes int) {
	t.Helper()
	bin := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("ffprobe", "echo 10.0\n")
	write("ffmpeg", `case "$*" in *h264_nvenc*) echo "no NVENC device" >&2; exit 1;; esac
for a in "$@"; do out="$a"; done
echo out_time_us=2500000; echo progress=continue
echo out_time_us=5000000; echo progress=continue
echo out_time_us=10000000; echo progress=end
head -c `+itoa(outBytes)+` /dev/zero > "$out"
`)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
}

func itoa(n int) string { return strings.TrimSpace(strings.Repeat(" ", 0) + fmtInt(n)) }
func fmtInt(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// drive runs commands and feeds their messages back into the model until
// pred holds or it runs out of work.
func drive(t *testing.T, m Model, cmd tea.Cmd, pred func(Model) bool) Model {
	t.Helper()
	queue := []tea.Cmd{cmd}
	deadline := time.Now().Add(10 * time.Second)
	for len(queue) > 0 && time.Now().Before(deadline) {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		msg := c()
		if b, ok := msg.(tea.BatchMsg); ok {
			queue = append(queue, b...)
			continue
		}
		switch msg.(type) {
		case compressProgressMsg, compressDoneMsg:
		default:
			continue // only compression traffic matters here (blink etc. would loop)
		}
		nm, next := m.Update(msg)
		m = nm.(Model)
		if pred(m) {
			return m
		}
		queue = append(queue, next)
	}
	return m
}

func TestOversizedVideoIsCompressedAndAttached(t *testing.T) {
	fakeTools(t, 600_000) // compressed result: 600 KB
	dir := t.TempDir()
	src := filepath.Join(dir, "screen recording.mp4")
	if err := os.WriteFile(src, make([]byte, 2_000_000), 0o600); err != nil {
		t.Fatal(err)
	}
	m := resize(fixture(), 140, 40)
	m.open = 1
	m.cfg.MaxAttach = 1_000_000 // 1 MB limit
	m.cfg.CacheDir = filepath.Join(t.TempDir(), "attachments")
	m.focus = focusInput
	m.cfg.DryRun = true // the queued send must not touch a real chat

	nm, cmd := m.Update(pasteKey("file://" + strings.ReplaceAll(src, " ", "%20")))
	m = nm.(Model)
	if m.compress == nil || !strings.Contains(m.status, "compressing") {
		t.Fatalf("compression did not start: %q", m.status)
	}
	if !strings.Contains(stripANSI(m.renderComposeRule(120)), "compressing screen recording.mp4") {
		t.Fatalf("compose line does not show compression: %q", stripANSI(m.renderComposeRule(120)))
	}
	// Enter during compression queues the send instead of dropping it.
	m.ta.SetValue("here it is")
	if c := m.send(); c != nil || !m.sendWhenReady || !strings.Contains(m.status, "will send") {
		t.Fatalf("enter during compression: sendWhenReady=%v status=%q", m.sendWhenReady, m.status)
	}
	m = drive(t, m, cmd, func(m Model) bool { return m.compress == nil })
	// The queued send ran (dry-run): text + the compressed file, compose cleared.
	if !strings.Contains(m.status, "would send 10 chars + screen recording-compressed.mp4") || len(m.attach) != 0 || m.sendWhenReady {
		t.Fatalf("queued send did not run: status=%q attach=%v", m.status, m.attach)
	}
}

func TestCompressedStillTooBigAndCancel(t *testing.T) {
	fakeTools(t, 1_500_000) // "compressed" result still over the 1 MB limit
	src := filepath.Join(t.TempDir(), "big.mov")
	os.WriteFile(src, make([]byte, 2_000_000), 0o600)
	m := resize(fixture(), 140, 40)
	m.open = 1
	m.cfg.MaxAttach = 1_000_000
	m.cfg.CacheDir = filepath.Join(t.TempDir(), "attachments")
	cmd := m.startCompress(src)
	m = drive(t, m, cmd, func(m Model) bool { return m.compress == nil })
	if len(m.attach) != 0 || !strings.Contains(m.status, "still") || !m.statusErr {
		t.Fatalf("oversized result: attach=%v status=%q", m.attach, m.status)
	}

	// ctrl+x cancels a running compression.
	m.startCompress(src)
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlX})
	if !strings.Contains(nm.(Model).status, "cancelling") {
		t.Fatalf("ctrl+x: %q", nm.(Model).status)
	}
}

func TestErrorsGetTheWholeStatusBar(t *testing.T) {
	m := resize(fixture(), 100, 30)
	m.focus = focusInput
	m.setStatus("attach: screenrecording-2026-10-23_22-43-44.mp4 is 972.3 MB, over the 100 MB limit", true)
	bar := stripANSI(m.renderStatus())
	if !strings.Contains(bar, "over the 100 MB limit") {
		t.Fatalf("error truncated: %q", bar)
	}
}

func TestOutgoingCleanupOnlyTouchesCompressedCopies(t *testing.T) {
	m := resize(fixture(), 100, 30)
	m.cfg.CacheDir = filepath.Join(t.TempDir(), "attachments")
	os.MkdirAll(m.outgoingDir(), 0o700)
	ours := filepath.Join(m.outgoingDir(), "clip-compressed.mp4")
	users := filepath.Join(t.TempDir(), "clip.mp4")
	os.WriteFile(ours, []byte("x"), 0o600)
	os.WriteFile(users, []byte("x"), 0o600)
	m.cleanupOutgoing([]string{ours, users})
	if _, err := os.Stat(ours); !os.IsNotExist(err) {
		t.Fatal("compressed copy not removed")
	}
	if _, err := os.Stat(users); err != nil {
		t.Fatal("the user's own file was removed")
	}
}

// fakeCrashyTools: ffmpeg segfaults on its first NVENC run (like the real
// intermittent decoder crash), then succeeds on the retry.
func fakeCrashyTools(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	state := filepath.Join(bin, "runs")
	os.WriteFile(filepath.Join(bin, "ffprobe"), []byte("#!/bin/sh\necho 10.0\n"), 0o755)
	os.WriteFile(filepath.Join(bin, "ffmpeg"), []byte(`#!/bin/sh
echo run >> "`+state+`"
n=$(wc -l < "`+state+`")
case "$*" in *thread_type*) echo safe >> "`+state+`.safe";; esac
if [ "$n" -le 2 ]; then kill -SEGV $$; fi
for a in "$@"; do out="$a"; done
echo out_time_us=10000000; echo progress=end
head -c 500000 /dev/zero > "$out"
`), 0o755)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	return state
}

func TestCrashIsRetried(t *testing.T) {
	state := fakeCrashyTools(t)
	src := filepath.Join(t.TempDir(), "clip.mp4")
	os.WriteFile(src, make([]byte, 2_000_000), 0o600)
	m := resize(fixture(), 140, 40)
	m.open = 1
	m.cfg.MaxAttach = 1_000_000
	m.cfg.CacheDir = filepath.Join(t.TempDir(), "attachments")
	m = drive(t, m, m.startCompress(src), func(m Model) bool { return m.compress == nil })
	b, _ := os.ReadFile(state)
	if runs := strings.Count(string(b), "run"); runs != 3 {
		t.Fatalf("want 2 crashes + 1 successful retry, got %d runs", runs)
	}
	if _, err := os.Stat(state + ".safe"); err != nil {
		t.Fatal("third attempt did not use the crash-safe decoder")
	}
	if len(m.attach) != 1 {
		t.Fatalf("not attached after retry: %q", m.status)
	}
}

func TestClickPendingAttachmentPreviewsIt(t *testing.T) {
	bin := t.TempDir()
	log := filepath.Join(bin, "opened")
	os.WriteFile(filepath.Join(bin, "xdg-open"), []byte("#!/bin/sh\necho \"$1\" >> '"+log+"'\n"), 0o755)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	vid := filepath.Join(t.TempDir(), "clip.mp4")
	os.WriteFile(vid, []byte("x"), 0o600)

	m := resize(fixture(), 140, 40)
	m.open = 1
	m.attach = []string{vid}
	m.layout()
	rule := stripANSI(m.renderComposeRule(120))
	if !strings.Contains(rule, "enter sends · click to preview · ctrl+x removes") {
		t.Fatalf("attachment line wording: %q", rule)
	}
	listW, _, _, vpH := m.dims()
	m = click(m, listW+10, 1+vpH) // the attachment line sits right above the compose box
	time.Sleep(300 * time.Millisecond)
	b, _ := os.ReadFile(log)
	if strings.TrimSpace(string(b)) != vid || !strings.Contains(m.status, "previewing clip.mp4") {
		t.Fatalf("preview not opened: xdg-open got %q, status %q", b, m.status)
	}
	if len(m.attach) != 1 {
		t.Fatal("preview must not remove the attachment")
	}
}
