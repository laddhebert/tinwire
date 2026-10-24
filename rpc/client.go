// Package rpc is a minimal JSON-RPC 2.0 client for line-delimited stdio servers
// such as `imsg rpc`, usually reached through an ssh child process.
package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ErrClosed is returned for calls made after the server process exited.
var ErrClosed = errors.New("rpc: connection closed")

// Error is a JSON-RPC error object returned by the server.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string { return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message) }

// Notification is a server-initiated message without an id.
type Notification struct {
	Method string
	Params json.RawMessage
}

type response struct {
	result json.RawMessage
	err    *Error
}

// Client owns one server process and multiplexes calls over its stdin/stdout.
type Client struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	wmu    sync.Mutex
	nextID atomic.Int64

	pmu     sync.Mutex
	pending map[int64]chan response

	// Notifications are queued without bound by readLoop and handed to the
	// consumer by pump, so a slow consumer never stalls call responses.
	qmu   sync.Mutex
	queue []Notification
	qsig  chan struct{}
	notes chan Notification

	done      chan struct{}
	err       error // set before done is closed
	quit      chan struct{}
	closeOnce sync.Once

	stderr *tail
}

// Start launches name with args and begins reading its stdout.
func Start(name string, args ...string) (*Client, error) {
	cmd := exec.Command(name, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	c := &Client{
		cmd:     cmd,
		stdin:   stdin,
		pending: map[int64]chan response{},
		notes:   make(chan Notification),
		qsig:    make(chan struct{}, 1),
		done:    make(chan struct{}),
		quit:    make(chan struct{}),
		stderr:  &tail{max: 20},
	}
	cmd.Stderr = c.stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go c.readLoop(stdout)
	go c.pump()
	return c, nil
}

// pump forwards queued notifications in order. It closes notes once the
// connection has ended and the queue is drained, or immediately on Close.
func (c *Client) pump() {
	defer close(c.notes)
	ended := false
	for {
		c.qmu.Lock()
		if len(c.queue) > 0 {
			n := c.queue[0]
			c.queue = c.queue[1:]
			c.qmu.Unlock()
			select {
			case c.notes <- n:
			case <-c.quit:
				return
			}
			continue
		}
		c.qmu.Unlock()
		if ended {
			return
		}
		select {
		case <-c.qsig:
		case <-c.done:
			ended = true // drain what is left, then close
		case <-c.quit:
			return
		}
	}
}

// Notifications delivers server notifications in order. It is closed after the
// connection ends and every queued notification has been delivered.
func (c *Client) Notifications() <-chan Notification { return c.notes }

// Done is closed when the server process has exited or stdout ended.
func (c *Client) Done() <-chan struct{} { return c.done }

// Err reports why the connection ended. Only valid after Done is closed.
func (c *Client) Err() error { return c.err }

// Stderr returns the last lines the server wrote to stderr.
func (c *Client) Stderr() string { return c.stderr.String() }

// Call sends a request and decodes the result into out (which may be nil).
func (c *Client) Call(ctx context.Context, method string, params, out any) error {
	id := c.nextID.Add(1)
	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	line, err := json.Marshal(req)
	if err != nil {
		return err
	}

	ch := make(chan response, 1)
	c.pmu.Lock()
	if c.pending == nil {
		c.pmu.Unlock()
		return ErrClosed
	}
	c.pending[id] = ch
	c.pmu.Unlock()
	defer func() {
		c.pmu.Lock()
		if c.pending != nil {
			delete(c.pending, id)
		}
		c.pmu.Unlock()
	}()

	c.wmu.Lock()
	_, err = c.stdin.Write(append(line, '\n'))
	c.wmu.Unlock()
	if err != nil {
		return fmt.Errorf("rpc: write %s: %w", method, err)
	}

	select {
	case r := <-ch:
		if r.err != nil {
			return r.err
		}
		if out == nil {
			return nil
		}
		return json.Unmarshal(r.result, out)
	case <-ctx.Done():
		return fmt.Errorf("rpc: %s: %w", method, ctx.Err())
	case <-c.done:
		// A response may have raced the shutdown.
		select {
		case r := <-ch:
			if r.err != nil {
				return r.err
			}
			if out != nil {
				return json.Unmarshal(r.result, out)
			}
			return nil
		default:
			return ErrClosed
		}
	}
}

// Close ends the session: closing stdin lets `imsg rpc` drain and exit, then
// the process is killed if it has not exited within the grace period.
func (c *Client) Close() error {
	c.closeOnce.Do(func() { close(c.quit) })
	c.wmu.Lock()
	_ = c.stdin.Close()
	c.wmu.Unlock()
	select {
	case <-c.done:
	case <-time.After(3 * time.Second):
		_ = c.cmd.Process.Kill()
		<-c.done
	}
	return nil
}

type envelope struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *Error          `json:"error"`
}

func (c *Client) readLoop(stdout io.Reader) {
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64*1024), 32*1024*1024)
	for sc.Scan() {
		var env envelope
		if err := json.Unmarshal(sc.Bytes(), &env); err != nil {
			continue // stdout is JSON-only by contract; ignore anything else
		}
		if env.Method != "" && (len(env.ID) == 0 || string(env.ID) == "null") {
			c.qmu.Lock()
			c.queue = append(c.queue, Notification{Method: env.Method, Params: env.Params})
			c.qmu.Unlock()
			select {
			case c.qsig <- struct{}{}:
			default:
			}
			continue
		}
		var id int64
		if err := json.Unmarshal(env.ID, &id); err != nil {
			continue
		}
		c.pmu.Lock()
		ch := c.pending[id]
		c.pmu.Unlock()
		if ch != nil {
			ch <- response{result: env.Result, err: env.Error}
		}
	}
	waitErr := c.cmd.Wait()
	switch {
	case sc.Err() != nil:
		c.err = sc.Err()
	case waitErr != nil:
		c.err = waitErr
	default:
		c.err = io.EOF
	}
	if s := strings.TrimSpace(c.stderr.String()); s != "" {
		c.err = fmt.Errorf("%w: %s", c.err, s)
	}
	c.pmu.Lock()
	c.pending = nil
	c.pmu.Unlock()
	close(c.done)
}

// tail keeps the last max lines written to it.
type tail struct {
	mu    sync.Mutex
	max   int
	lines []string
	part  string
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.part + string(p)
	parts := strings.Split(s, "\n")
	t.part = parts[len(parts)-1]
	for _, l := range parts[:len(parts)-1] {
		if l = strings.TrimSpace(l); l != "" {
			t.lines = append(t.lines, l)
		}
	}
	if len(t.lines) > t.max {
		t.lines = t.lines[len(t.lines)-t.max:]
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := strings.Join(t.lines, "\n")
	if p := strings.TrimSpace(t.part); p != "" {
		out = strings.TrimSpace(out + "\n" + p)
	}
	return out
}
