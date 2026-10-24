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
	a := strings.Join(ffmpegArgs("libx264", "in.mov", "out.mp4", 1800), " ")
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

	nm, cmd := m.Update(pasteKey("file://" + strings.ReplaceAll(src, " ", "%20")))
	m = nm.(Model)
	if m.compress == nil || !strings.Contains(m.status, "compressing") {
		t.Fatalf("compression did not start: %q", m.status)
	}
	if c := m.send(); c != nil || !strings.Contains(m.status, "still being compressed") {
		t.Fatalf("send allowed during compression: %q", m.status)
	}
	m = drive(t, m, cmd, func(m Model) bool { return m.compress == nil })
	if len(m.attach) != 1 || !strings.HasSuffix(m.attach[0], "screen recording-compressed.mp4") {
		t.Fatalf("compressed video not attached: %v status=%q", m.attach, m.status)
	}
	if !strings.Contains(m.status, "compressed screen recording.mp4") {
		t.Fatalf("status = %q", m.status)
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
