package recording

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/askidmobile/srvmon/internal/secret"
)

func testBox(t *testing.T) *secret.Box {
	t.Helper()
	k := make([]byte, 32)
	rand.Read(k)
	b, err := secret.New(k)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func readAll(t *testing.T, path, id string, box *secret.Box) []string {
	t.Helper()
	rc, err := Open(path, id, box)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	var lines []string
	sc := bufio.NewScanner(rc)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return lines
}

func TestRoundTrip(t *testing.T) {
	box := testBox(t)
	path := filepath.Join(t.TempDir(), "a.rec")
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	r, err := Create(Options{Path: path, ID: "abc", Box: box, Cols: 80, Rows: 24, Title: "srvmon@web", Now: c.now})
	if err != nil {
		t.Fatal(err)
	}
	r.Output([]byte("hello "))
	c.t = c.t.Add(1500 * time.Millisecond)
	// "привет" split in the middle of a two-byte letter.
	word := []byte("привет\r\n")
	r.Output(word[:3])
	r.Output(word[3:])
	r.Resize(100, 30)
	st, err := r.Close()
	if err != nil {
		t.Fatal(err)
	}
	if st.Bytes != int64(len("hello ")+len(word)) || st.Duration != 1500*time.Millisecond || st.Truncated {
		t.Fatalf("stats %+v", st)
	}

	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "hello") || strings.Contains(string(raw), "привет") {
		t.Fatal("plaintext in the file")
	}

	lines := readAll(t, path, "abc", box)
	var h Header
	if err := json.Unmarshal([]byte(lines[0]), &h); err != nil || h.Version != 2 || h.Width != 80 || h.Height != 24 {
		t.Fatalf("header %q %v", lines[0], err)
	}
	var out strings.Builder
	var sawResize bool
	for _, l := range lines[1:] {
		var ev []any
		if err := json.Unmarshal([]byte(l), &ev); err != nil || len(ev) != 3 {
			t.Fatalf("event %q: %v", l, err)
		}
		switch ev[1] {
		case "o":
			out.WriteString(ev[2].(string))
		case "r":
			sawResize = ev[2] == "100x30"
		}
	}
	if out.String() != "hello привет\r\n" || !sawResize {
		t.Fatalf("output %q resize %v", out.String(), sawResize)
	}
	if !strings.HasPrefix(lines[2], "[1.500000, \"o\"") {
		t.Fatalf("timing: %q", lines[2])
	}
}

func TestWrongIDOrKeyFails(t *testing.T) {
	box := testBox(t)
	path := filepath.Join(t.TempDir(), "a.rec")
	r, _ := Create(Options{Path: path, ID: "one", Box: box, Cols: 80, Rows: 24})
	r.Output([]byte("secret"))
	r.Close()
	for name, open := range map[string]func() (io.ReadCloser, error){
		"id":  func() (io.ReadCloser, error) { return Open(path, "two", box) },
		"key": func() (io.ReadCloser, error) { return Open(path, "one", testBox(t)) },
	} {
		rc, err := open()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadAll(rc); err == nil {
			t.Errorf("%s: read succeeded", name)
		}
		rc.Close()
	}
}

func TestSizeLimit(t *testing.T) {
	box := testBox(t)
	path := filepath.Join(t.TempDir(), "a.rec")
	r, _ := Create(Options{Path: path, ID: "x", Box: box, Cols: 80, Rows: 24, MaxBytes: 10})
	r.Output([]byte("12345678"))
	r.Output([]byte("abcdef"))
	r.Output([]byte("more"))
	st, _ := r.Close()
	if !st.Truncated || st.Bytes != 10 {
		t.Fatalf("stats %+v", st)
	}
	all := strings.Join(readAll(t, path, "x", box), "\n")
	if !strings.Contains(all, `"ab"`) || strings.Contains(all, "more") || !strings.Contains(all, "size limit") {
		t.Fatalf("content %s", all)
	}
}

func TestCrashCutFramePlaysEarlierChunks(t *testing.T) {
	box := testBox(t)
	path := filepath.Join(t.TempDir(), "a.rec")
	r, _ := Create(Options{Path: path, ID: "x", Box: box, Cols: 80, Rows: 24})
	r.Output([]byte(strings.Repeat("a", chunkSize))) // forces a chunk
	r.Output([]byte("tail"))
	r.Close()
	fi, _ := os.Stat(path)
	if err := os.Truncate(path, fi.Size()-5); err != nil {
		t.Fatal(err)
	}
	lines := readAll(t, path, "x", box)
	if len(lines) != 2 || strings.Contains(strings.Join(lines, ""), "tail") {
		t.Fatalf("got %d lines", len(lines))
	}
}

func TestPeriodicFlush(t *testing.T) {
	box := testBox(t)
	path := filepath.Join(t.TempDir(), "a.rec")
	old := flushEvery
	flushEvery = 50 * time.Millisecond
	defer func() { flushEvery = old }()
	r, _ := Create(Options{Path: path, ID: "x", Box: box, Cols: 80, Rows: 24})
	defer r.Close()
	r.Output([]byte("ls\r\n"))
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if fi, _ := os.Stat(path); fi.Size() > int64(len(magic)) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("nothing flushed while the session runs")
}

func TestUTF8Cut(t *testing.T) {
	for _, c := range []struct {
		in   string
		want int
	}{
		{"abc", 3},
		{"аб", 4},
		{"а\xd0", 2},
		{"x\xe2\x82", 1},
		{"x\xe2\x82\xac", 4},
		{"\xf0\x9f\x98", 0},
		{"\x80\x80", 2}, // garbage passes through
	} {
		if got := utf8Cut([]byte(c.in)); got != c.want {
			t.Errorf("%q: %d, want %d", c.in, got, c.want)
		}
	}
}

func TestWriteFailureStopsBuffering(t *testing.T) {
	box := testBox(t)
	path := filepath.Join(t.TempDir(), "a.rec")
	r, _ := Create(Options{Path: path, ID: "x", Box: box, Cols: 80, Rows: 24})
	r.f.Close() // every write fails from now on, like a full disk
	r.Output([]byte(strings.Repeat("a", chunkSize)))
	r.Output([]byte(strings.Repeat("b", chunkSize)))
	r.mu.Lock()
	n, stopped := r.buf.Len(), r.stopped
	r.mu.Unlock()
	if !stopped || n != 0 {
		t.Fatalf("stopped %v, %d bytes buffered", stopped, n)
	}
	if _, err := r.Close(); err == nil {
		t.Fatal("write error not reported")
	}
}
