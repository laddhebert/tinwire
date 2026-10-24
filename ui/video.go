package ui

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Videos over the attachment limit are compressed with ffmpeg so they fit:
// H.264 + AAC in MP4, at most 1080p and 30 fps, at the bitrate that lands
// the file at ~90% of the limit. NVENC is tried first, then libx264.

var videoExts = map[string]bool{
	".mp4": true, ".mov": true, ".m4v": true, ".mkv": true, ".webm": true,
	".avi": true, ".mts": true, ".m2ts": true, ".3gp": true, ".wmv": true, ".flv": true,
}

func isVideo(p string) bool { return videoExts[strings.ToLower(filepath.Ext(p))] }

const (
	audioKbps    = 96
	minVideoKbps = 250 // below this a video is not worth sending; ask to trim instead
)

type compressProgressMsg struct {
	job int
	pct int
}

type compressDoneMsg struct {
	job      int
	src, out string
	err      error
}

// compressJob is the running compression, if any.
type compressJob struct {
	id     int
	src    string
	cancel context.CancelFunc
	ch     chan tea.Msg
}

// videoBitrate returns the video bitrate (kbit/s) that makes a clip of the
// given duration come out at targetBytes, or an error when it cannot fit.
func videoBitrate(targetBytes int64, dur time.Duration) (int, error) {
	secs := dur.Seconds()
	if secs <= 0 {
		return 0, errors.New("could not read the video's duration")
	}
	total := int(float64(targetBytes) * 8 / secs / 1000)
	v := total - audioKbps
	if v < minVideoKbps {
		return 0, fmt.Errorf("a %s video cannot fit in %s at watchable quality; trim it first",
			dur.Round(time.Second), humanBytes(targetBytes))
	}
	return v, nil
}

func probeDuration(ctx context.Context, path string) (time.Duration, error) {
	out, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1", path).Output()
	if err != nil {
		return 0, fmt.Errorf("ffprobe: %w", err)
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		return 0, errors.New("could not read the video's duration")
	}
	return time.Duration(f * float64(time.Second)), nil
}

// ffmpegArgs builds the command. safeDecode switches the decoder to slice
// threading: ffmpeg 9.0.2's frame-threaded H.264 decoder crashed
// intermittently (SIGSEGV in its frame threads) on a 1 GB screen recording,
// but slice threading decodes such files ~4x slower (114 s vs 27 s), so it is
// only used after crashes.
func ffmpegArgs(encoder, src, out string, vKbps int, safeDecode bool) []string {
	args := []string{"-y", "-hide_banner", "-loglevel", "error"}
	if safeDecode {
		args = append(args, "-thread_type", "slice")
	}
	args = append(args, "-i", src,
		"-map", "0:v:0", "-map", "0:a:0?",
		"-vf", "scale='min(1920,iw)':'min(1080,ih)':force_original_aspect_ratio=decrease,scale=trunc(iw/2)*2:trunc(ih/2)*2",
		"-fpsmax", "30", "-pix_fmt", "yuv420p",
		"-c:v", encoder,
		"-b:v", fmt.Sprintf("%dk", vKbps), "-maxrate", fmt.Sprintf("%dk", vKbps), "-bufsize", fmt.Sprintf("%dk", 2*vKbps))
	if encoder == "libx264" {
		args = append(args, "-preset", "veryfast")
	} else {
		args = append(args, "-preset", "p5")
	}
	return append(args, "-c:a", "aac", "-b:a", fmt.Sprintf("%dk", audioKbps),
		"-movflags", "+faststart", "-progress", "pipe:1", "-nostats", out)
}

// startCompress begins compressing src in the background.
func (m *Model) startCompress(src string) tea.Cmd {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		m.setStatus(fmt.Sprintf("attach: %s is over the %s limit; install ffmpeg to have videos compressed automatically",
			filepath.Base(src), humanBytes(m.cfg.MaxAttach)), true)
		return nil
	}
	if m.compress != nil {
		m.compressQueue = append(m.compressQueue, src)
		return nil
	}
	m.compressSeq++
	ctx, cancel := context.WithCancel(context.Background())
	job := &compressJob{id: m.compressSeq, src: src, cancel: cancel, ch: make(chan tea.Msg, 32)}
	m.compress = job
	target := m.cfg.MaxAttach * 9 / 10
	outDir := m.outgoingDir()
	m.setStatus("compressing "+filepath.Base(src)+" to fit iMessage's limit… (ctrl+x cancels)", false)
	go runCompress(ctx, job, src, outDir, target)
	return waitCompress(job.ch)
}

func waitCompress(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

func runCompress(ctx context.Context, job *compressJob, src, outDir string, target int64) {
	done := func(out string, err error) {
		job.ch <- compressDoneMsg{job: job.id, src: src, out: out, err: err}
		close(job.ch)
	}
	dur, err := probeDuration(ctx, src)
	if err != nil {
		done("", err)
		return
	}
	vKbps, err := videoBitrate(target, dur)
	if err != nil {
		done("", err)
		return
	}
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		done("", err)
		return
	}
	base := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
	out := filepath.Join(outDir, base+"-compressed.mp4")
	var lastErr error
encoders:
	for _, enc := range []string{"h264_nvenc", "libx264"} {
		// A crash (killed by a signal) is retried with the same encoder, the
		// last time with the slower crash-safe decoder; any other failure
		// moves on to the next encoder.
		for attempt := 1; attempt <= 3; attempt++ {
			if ctx.Err() != nil {
				break encoders
			}
			lastErr = runFFmpeg(ctx, job, enc, src, out, vKbps, dur, attempt == 3)
			if lastErr == nil {
				break encoders
			}
			os.Remove(out)
			if !crashed(lastErr) {
				break
			}
		}
	}
	if ctx.Err() != nil {
		os.Remove(out)
		done("", context.Canceled)
		return
	}
	if lastErr != nil {
		done("", fmt.Errorf("ffmpeg: %w", lastErr))
		return
	}
	done(out, nil)
}

// crashed reports whether ffmpeg was killed by a signal (e.g. SIGSEGV) rather
// than exiting with an error.
func crashed(err error) bool {
	var ee *exec.ExitError
	return errors.As(err, &ee) && ee.ProcessState != nil && ee.ProcessState.ExitCode() == -1
}

func runFFmpeg(ctx context.Context, job *compressJob, enc, src, out string, vKbps int, dur time.Duration, safeDecode bool) error {
	cmd := exec.CommandContext(ctx, "ffmpeg", ffmpegArgs(enc, src, out, vKbps, safeDecode)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(stdout)
	last := -1
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok || k != "out_time_us" {
			continue
		}
		us, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil || dur <= 0 {
			continue
		}
		pct := int(float64(us) / float64(dur.Microseconds()) * 100)
		if pct > 99 {
			pct = 99
		}
		if pct != last {
			last = pct
			select {
			case job.ch <- compressProgressMsg{job: job.id, pct: pct}:
			default: // UI is behind; skip this tick
			}
		}
	}
	if err := cmd.Wait(); err != nil {
		if s := strings.TrimSpace(stderr.String()); s != "" {
			lines := strings.Split(s, "\n")
			return fmt.Errorf("%s: %s: %w", enc, lines[len(lines)-1], err)
		}
		return fmt.Errorf("%s: %w", enc, err)
	}
	return nil
}

// outgoingDir holds videos compressed by tinwire. Only files in it are ever
// deleted by cleanupOutgoing; files the user attached are never touched.
func (m Model) outgoingDir() string { return filepath.Join(filepath.Dir(m.cfg.CacheDir), "outgoing") }

func (m Model) cleanupOutgoing(files []string) {
	dir := m.outgoingDir() + string(os.PathSeparator)
	for _, f := range files {
		if strings.HasPrefix(f, dir) {
			os.Remove(f)
		}
	}
}

// previewLocal opens files that are about to be sent (local paths) in the
// default viewer, so a video can be checked before sending.
func (m *Model) previewLocal(paths []string) tea.Cmd {
	var opened, skipped []string
	for _, p := range paths {
		if !openable[strings.ToLower(filepath.Ext(p))] {
			skipped = append(skipped, filepath.Base(p))
			continue
		}
		if err := startDetached("xdg-open", p); err != nil {
			m.setStatus("preview: xdg-open: "+err.Error(), true)
			return nil
		}
		opened = append(opened, filepath.Base(p))
	}
	msg := ""
	if len(opened) > 0 {
		msg = "previewing " + strings.Join(opened, ", ") + " · enter sends"
	}
	if len(skipped) > 0 {
		if msg != "" {
			msg += " · "
		}
		msg += "no preview for " + strings.Join(skipped, ", ")
	}
	m.setStatus(msg, false)
	return nil
}

// cancelCompress stops a running compression and drops queued ones.
func (m *Model) cancelCompress() bool {
	if m.compress == nil && len(m.compressQueue) == 0 {
		return false
	}
	if m.compress != nil {
		m.compress.cancel()
	}
	m.compressQueue = nil
	m.sendWhenReady = false
	return true
}

// handleCompress processes compression messages from update().
func (m *Model) handleCompress(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case compressProgressMsg:
		if m.compress == nil || msg.job != m.compress.id {
			return nil, true
		}
		m.compressPct = msg.pct
		m.setStatus(fmt.Sprintf("compressing %s… %d%% (ctrl+x cancels)", filepath.Base(m.compress.src), msg.pct), false)
		return waitCompress(m.compress.ch), true
	case compressDoneMsg:
		if m.compress == nil || msg.job != m.compress.id {
			return nil, true
		}
		m.compress = nil
		m.compressPct = 0
		var cmds []tea.Cmd
		switch {
		case errors.Is(msg.err, context.Canceled):
			m.sendWhenReady = false
			m.setStatus("video compression cancelled", false)
		case msg.err != nil:
			m.sendWhenReady = false
			m.setStatus("attach: "+filepath.Base(msg.src)+": "+msg.err.Error(), true)
		default:
			if st, err := os.Stat(msg.out); err == nil && st.Size() > m.cfg.MaxAttach {
				m.setStatus(fmt.Sprintf("attach: compressed video is still %s, over the limit; trim it first", humanBytes(st.Size())), true)
				os.Remove(msg.out)
			} else {
				cmds = append(cmds, m.addAttachments([]string{msg.out}))
				if st != nil {
					m.setStatus(fmt.Sprintf("compressed %s to %s and attached it · enter sends", filepath.Base(msg.src), humanBytes(st.Size())), false)
				}
			}
		}
		if len(m.compressQueue) > 0 {
			next := m.compressQueue[0]
			m.compressQueue = m.compressQueue[1:]
			cmds = append(cmds, m.startCompress(next))
		} else if m.sendWhenReady && msg.err == nil {
			m.sendWhenReady = false
			cmds = append(cmds, m.send())
		}
		return tea.Batch(cmds...), true
	}
	return nil, false
}
