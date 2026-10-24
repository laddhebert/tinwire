// tinwire is a terminal iMessage client for Linux. It drives `imsg rpc` on a
// Mac that is signed into Messages, over ssh: nothing listens on the network,
// there is no cloud relay, and SIP stays on (AppleScript send).
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/laddhebert/tinwire/imsg"
	"github.com/laddhebert/tinwire/keyfix"
	"github.com/laddhebert/tinwire/remote"
	"github.com/laddhebert/tinwire/rpc"
	"github.com/laddhebert/tinwire/ui"
)

var version = "dev"

const configHelp = `tinwire needs to know which Mac to connect to.

Create ~/.config/tinwire/config with:

    host = you@your-mac.local

(any ssh destination that works with key auth, e.g. a Host alias from
~/.ssh/config), or set TINWIRE_HOST, or pass --host. See the README for the
one-time Mac setup.`

func main() {
	home, _ := os.UserHomeDir()
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}
	cfgFile, err := loadKV(filepath.Join(configHome, "tinwire", "config"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}
	cacheHome := os.Getenv("XDG_CACHE_HOME")
	if cacheHome == "" {
		cacheHome = filepath.Join(home, ".cache")
	}
	var (
		host       = flag.String("host", envOr("TINWIRE_HOST", cfgFile["host"]), "ssh destination of the Mac running Messages, or \"local\" to run imsg directly (config: host)")
		imsgPath   = flag.String("imsg", envOr("TINWIRE_IMSG", orDefault(cfgFile["imsg"], "/usr/local/bin/imsg")), "path to imsg (>= 0.19) on the Mac (config: imsg)")
		chats      = flag.Int("chats", 100, "number of chats to list")
		history    = flag.Int("history", 200, "messages per page of history")
		maxSends   = flag.Int("max-sends", 0, "optional cap on messages sent per run (0 = no cap)")
		dryRun     = flag.Bool("dry-run", false, "never send, react, mark read or show typing; show what would happen")
		timeout    = flag.Duration("timeout", 20*time.Second, "timeout for read calls")
		sendTO     = flag.Duration("send-timeout", 90*time.Second, "timeout for a send (AppleScript plus receipt check)")
		contacts   = flag.String("contacts", filepath.Join(configHome, "tinwire", "contacts"), "optional handle-to-name overrides (\"+15551234567 = Mom\" per line)")
		noContacts = flag.Bool("no-contacts", false, "do not read names from the Mac's AddressBook")
		noNotify   = flag.Bool("no-notify", false, "no desktop notifications")
		noRead     = flag.Bool("no-mark-read", false, "do not mark chats read on the Mac (no read receipts)")
		noTyping   = flag.Bool("no-typing", false, "do not send typing indicators")
		noMouse    = flag.Bool("no-mouse", false, "disable mouse support (keeps the terminal's own text selection)")
		noDropSend = flag.Bool("no-send-on-drop", false, "dropping/pasting a file only attaches it; enter sends")
		debugLog   = flag.String("debug-log", "", "append UI event diagnostics (no message content) to this file")
		cacheDir   = flag.String("cache-dir", filepath.Join(cacheHome, "tinwire", "attachments"), "where downloaded attachments are cached")
		dlDir      = flag.String("download-dir", filepath.Join(home, "Downloads"), "where 's' saves attachments")
		stateDir   = flag.String("state-dir", filepath.Join(stateHome(home), "tinwire"), "local state (which chats you have read here)")
		check      = flag.Bool("check", false, "connect, print counts only (no message content), and exit")
		showVer    = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()
	if *host == "" && !*showVer {
		fmt.Fprintln(os.Stderr, configHelp)
		os.Exit(2)
	}
	if *showVer {
		fmt.Println("tinwire", version)
		return
	}

	mac := remote.Mac{Host: *host, Imsg: *imsgPath}
	connect := func() (*imsg.API, error) {
		var c *rpc.Client
		var err error
		if *host == "local" {
			c, err = rpc.Start(*imsgPath, "rpc")
		} else {
			args := append([]string{"-T"}, remote.SSHOptions...)
			args = append(args, *host, "--", remote.Quote(*imsgPath), "rpc")
			c, err = rpc.Start("ssh", args...)
		}
		if err != nil {
			return nil, err
		}
		return &imsg.API{C: c, Timeout: *timeout, SendTO: *sendTO}, nil
	}

	if *check {
		os.Exit(runCheck(connect, mac, *chats, !*noContacts))
	}

	aliases, err := loadAliases(*contacts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "contacts:", err)
		os.Exit(1)
	}
	if *debugLog != "" {
		f, err := os.OpenFile(*debugLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			fmt.Fprintln(os.Stderr, "debug log:", err)
			os.Exit(1)
		}
		defer f.Close()
		ui.SetDebugLog(f)
	}
	if err := os.MkdirAll(*cacheDir, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "cache dir:", err)
		os.Exit(1)
	}

	m := ui.New(ui.Config{
		Connect:      connect,
		Mac:          mac,
		Host:         *host,
		ChatLimit:    *chats,
		HistoryLimit: *history,
		MaxSends:     *maxSends,
		DryRun:       *dryRun,
		Aliases:      aliases,
		Contacts:     !*noContacts,
		Notify:       !*noNotify,
		MarkRead:     !*noRead,
		Typing:       !*noTyping,
		CacheDir:     *cacheDir,
		DownloadDir:  *dlDir,
		StateDir:     *stateDir,
		SendOnDrop:   !*noDropSend,
	})
	opts := []tea.ProgramOption{tea.WithAltScreen(), tea.WithReportFocus()}
	if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		// Rewrite Shift/Ctrl+Enter (CSI u) to alt+enter so they make a new line.
		opts = append(opts, tea.WithInput(keyfix.Wrap(os.Stdin)))
	}
	if !*noMouse {
		opts = append(opts, tea.WithMouseCellMotion())
	}
	if _, err := tea.NewProgram(m, opts...).Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// runCheck exercises the whole path (ssh, imsg, database, watch, contacts)
// and prints only counts so it is safe to run in logs.
func runCheck(connect func() (*imsg.API, error), mac remote.Mac, limit int, withContacts bool) int {
	fail := func(what string, err error) int {
		fmt.Fprintf(os.Stderr, "FAIL %s: %v\n", what, err)
		return 1
	}
	api, err := connect()
	if err != nil {
		return fail("connect", err)
	}
	defer api.C.Close()
	st, err := api.Status()
	if err != nil {
		return fail("status", err)
	}
	fmt.Printf("imsg %s, protocol %d, database ready=%v, send=%v typing=%v read=%v\n",
		st.Version, st.ProtocolVersion, st.Database.Ready, st.Has("send"), st.Has("typing"), st.Has("read"))
	if !st.Database.Ready {
		return fail("database", fmt.Errorf("%s", st.Database.Error))
	}
	cs, err := api.Chats(limit)
	if err != nil {
		return fail("chats.list", err)
	}
	fmt.Printf("chats: %d\n", len(cs))
	if len(cs) > 0 {
		ms, err := api.History(cs[0].ID, 20)
		if err != nil {
			return fail("messages.history", err)
		}
		att := 0
		for _, x := range ms {
			att += len(x.Attachments)
		}
		fmt.Printf("history of most recent chat: %d messages, %d attachments\n", len(ms), att)
		if len(ms) > 0 {
			older, err := api.HistoryBefore(cs[0].ID, 20, ms[0].CreatedAt.Time)
			if err != nil {
				return fail("messages.history (older page)", err)
			}
			fmt.Printf("older page: %d messages\n", len(older))
		}
	}
	if withContacts {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		names, err := mac.Contacts(ctx)
		cancel()
		if err != nil {
			return fail("contacts", err)
		}
		named := 0
		for _, c := range cs {
			for _, p := range c.Participants {
				found := false
				for _, k := range remote.HandleKeys(p) {
					if _, ok := names[k]; ok {
						found = true
					}
				}
				if found {
					named++
					break
				}
			}
		}
		fmt.Printf("contacts: %d lookup keys; %d of %d chats have a named participant\n", len(names), named, len(cs))
	}
	sub, err := api.Subscribe()
	if err != nil {
		return fail("watch.subscribe", err)
	}
	fmt.Printf("watch subscription: %d\nOK\n", sub)
	return 0
}

func loadAliases(path string) (map[string]string, error) {
	out := map[string]string{}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		h, name, ok := strings.Cut(line, "=")
		if !ok {
			h, name, ok = strings.Cut(line, "\t")
		}
		if !ok {
			return nil, fmt.Errorf("%s:%d: want \"handle = name\"", path, n)
		}
		h, name = strings.TrimSpace(h), strings.TrimSpace(name)
		out[h] = name
		for _, k := range remote.HandleKeys(h) {
			out[k] = name
		}
	}
	return out, sc.Err()
}

func stateHome(home string) string {
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return v
	}
	return filepath.Join(home, ".local", "state")
}

// loadKV reads "key = value" lines (# comments, blank lines ignored). A
// missing file is not an error.
func loadKV(path string) (map[string]string, error) {
	out := map[string]string{}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("%s:%d: want \"key = value\"", path, n)
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out, sc.Err()
}

func orDefault(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
