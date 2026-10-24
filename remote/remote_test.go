package remote

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestHandleKeys(t *testing.T) {
	cases := map[string][]string{
		"+1 (555) 123-4567": {"15551234567", "5551234567"},
		"5551234567":        {"5551234567"},
		"Jane@Example.COM":  {"jane@example.com"},
		"123":               nil,
	}
	for in, want := range cases {
		if got := HandleKeys(in); !reflect.DeepEqual(got, want) {
			t.Errorf("HandleKeys(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestSafeName(t *testing.T) {
	cases := map[string]string{
		"photo.jpg":                       "photo.jpg",
		"../../.bashrc":                   "bashrc",
		"a/b/c.txt":                       "c.txt",
		"evil\x00name\n.png":              "evil_name_.png",
		"":                                "attachment",
		"-rf":                             "rf",
		`C:\Users\x\doc.pdf`:              "doc.pdf",
		strings.Repeat("a", 300) + ".jpg": strings.Repeat("a", 116) + ".jpg",
	}
	for in, want := range cases {
		if got := SafeName(in); got != want {
			t.Errorf("SafeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAttachmentPathGuard(t *testing.T) {
	ok := []string{
		"/Users/alice/Library/Messages/Attachments/ab/01/X/IMG_1.heic",
	}
	bad := []string{
		"/etc/passwd",
		"/Users/alice/.ssh/id_ed25519",
		"/Users/alice/Library/Messages/Attachments/../../.ssh/id_ed25519",
		"~/Library/Messages/Attachments/x",
		"/Users/a/Library/Messages/chat.db",
	}
	for _, p := range ok {
		if err := checkAttachmentPath(p); err != nil {
			t.Errorf("%s rejected: %v", p, err)
		}
	}
	for _, p := range bad {
		if err := checkAttachmentPath(p); err == nil {
			t.Errorf("%s accepted", p)
		}
	}
	if _, err := (Mac{Host: "local"}).Download(context.Background(), "/etc/passwd", filepath.Join(t.TempDir(), "x")); err == nil {
		t.Fatal("Download accepted a path outside the attachment store")
	}
}

func TestQuoteRoundTrip(t *testing.T) {
	// Run through a real shell: the quoted value must come back unchanged.
	in := `it's "quoted" $HOME ; rm -rf / ` + "`x`"
	var out strings.Builder
	if err := (Mac{Host: "local"}).run(context.Background(), "printf %s "+Quote(in), nil, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != in {
		t.Fatalf("got %q", out.String())
	}
}

func TestUploadAndRemoveLocal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := filepath.Join(t.TempDir(), "note's file.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	mac := Mac{Host: "local"}
	remote, err := mac.Upload(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(remote, filepath.Join(home, "Library/Caches/tinwire/outbox")+"/") {
		t.Fatalf("uploaded outside outbox: %s", remote)
	}
	b, err := os.ReadFile(remote)
	if err != nil || string(b) != "hello" {
		t.Fatalf("content: %q %v", b, err)
	}
	if st, _ := os.Stat(remote); st.Mode().Perm() != 0o600 {
		t.Fatalf("outbox file mode %v", st.Mode().Perm())
	}
	if err := mac.RemoveUpload(context.Background(), remote); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(remote); !os.IsNotExist(err) {
		t.Fatal("upload not removed")
	}
	if err := mac.RemoveUpload(context.Background(), "/etc/hosts"); err == nil {
		t.Fatal("RemoveUpload accepted a path outside the outbox")
	}
}
