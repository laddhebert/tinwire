package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

// TestHelperProcess is a fake line-delimited JSON-RPC server, re-executed from
// the test binary.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("RPC_FAKE_SERVER") != "1" {
		return
	}
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		var req struct {
			ID     int64           `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.Unmarshal(sc.Bytes(), &req)
		switch req.Method {
		case "echo":
			fmt.Printf(`{"jsonrpc":"2.0","id":%d,"result":%s}`+"\n", req.ID, req.Params)
		case "notify":
			fmt.Println(`{"jsonrpc":"2.0","method":"message","params":{"subscription":1}}`)
			fmt.Printf(`{"jsonrpc":"2.0","id":%d,"result":{"ok":true}}`+"\n", req.ID)
		case "fail":
			fmt.Printf(`{"jsonrpc":"2.0","id":%d,"error":{"code":-32001,"message":"maybe","data":{"retry_safe":false}}}`+"\n", req.ID)
		case "hang":
			// never answer
		case "exit":
			fmt.Fprintln(os.Stderr, "fake server exiting")
			os.Exit(3)
		}
	}
	os.Exit(0)
}

func startFake(t *testing.T) *Client {
	t.Helper()
	t.Setenv("RPC_FAKE_SERVER", "1")
	c, err := Start(os.Args[0], "-test.run=TestHelperProcess")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return c
}

func TestCallAndNotification(t *testing.T) {
	c := startFake(t)
	var out struct{ N int }
	if err := c.Call(ctx(t), "echo", map[string]int{"n": 7}, &out); err != nil || out.N != 7 {
		t.Fatalf("echo: %v %+v", err, out)
	}
	if err := c.Call(ctx(t), "notify", nil, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case n := <-c.Notifications():
		if n.Method != "message" {
			t.Fatalf("got %q", n.Method)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no notification")
	}
}

func TestErrorObject(t *testing.T) {
	c := startFake(t)
	err := c.Call(ctx(t), "fail", nil, nil)
	var re *Error
	if !errors.As(err, &re) || re.Code != -32001 || len(re.Data) == 0 {
		t.Fatalf("want rpc error -32001 with data, got %v", err)
	}
}

func TestTimeout(t *testing.T) {
	c := startFake(t)
	cx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := c.Call(cx, "hang", nil, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline exceeded, got %v", err)
	}
}

func TestServerExit(t *testing.T) {
	c := startFake(t)
	_ = c.Call(ctx(t), "exit", nil, nil)
	select {
	case <-c.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("Done not closed after server exit")
	}
	if c.Err() == nil || c.Stderr() == "" {
		t.Fatalf("want exit error and stderr, got %v / %q", c.Err(), c.Stderr())
	}
	if err := c.Call(ctx(t), "echo", map[string]int{}, nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("want ErrClosed after exit, got %v", err)
	}
	if _, ok := <-c.Notifications(); ok {
		t.Fatal("notifications channel should be closed")
	}
}

func TestCloseWhileNobodyReadsNotifications(t *testing.T) {
	c := startFake(t)
	for i := 0; i < 300; i++ { // more than the channel buffer
		if err := c.Call(ctx(t), "notify", nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan struct{})
	go func() { c.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close deadlocked with an unread notification backlog")
	}
}
