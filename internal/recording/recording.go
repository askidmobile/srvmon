// Package recording writes web terminal sessions as asciinema v2 (asciicast)
// files, encrypted at rest with the panel's master key.
//
// Only terminal output and window resizes are recorded, never keystrokes:
// a password typed at a prompt that does not echo (sudo, passwd, ssh) stays
// out of the file. Whatever the server prints is in it, so a recording is as
// sensitive as the session and is sealed like the SSH keys are.
//
// File layout: the magic "SRVMREC1", then frames of a big-endian uint32
// length and a sealed chunk of the asciicast text. Each chunk is bound to the
// recording id and its position (AAD "srvmon-rec:<id>:<n>"), so chunks cannot
// be reordered, dropped from the middle or moved to another recording. A
// frame cut short by a crash ends the stream; the chunks before it still
// play.
package recording

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/askidmobile/srvmon/internal/secret"
)

const (
	magic = "SRVMREC1"
	// chunkSize is how much asciicast text is buffered before it is sealed
	// and written.
	chunkSize = 64 << 10
	maxFrame  = 4 << 20
)

// flushEvery bounds how long output may sit in memory unsealed.
var flushEvery = 5 * time.Second

// Stats describe a finished recording.
type Stats struct {
	Bytes     int64         // terminal output recorded, bytes
	Duration  time.Duration // first header to Close
	Truncated bool          // stopped at the size limit
}

// Recorder appends events of one terminal session. Methods are safe for
// concurrent use; after Close they do nothing.
type Recorder struct {
	mu      sync.Mutex
	f       *os.File
	box     *secret.Box
	id      string
	start   time.Time
	now     func() time.Time
	buf     bytes.Buffer
	partial []byte // incomplete UTF-8 sequence held back from the last write
	n       int    // chunks written
	bytes   int64
	max     int64
	stopped bool
	closed  bool
	err     error
	stop    chan struct{}
	done    chan struct{}
}

// Header is the first line of an asciicast v2 file.
type Header struct {
	Version   int               `json:"version"`
	Width     int               `json:"width"`
	Height    int               `json:"height"`
	Timestamp int64             `json:"timestamp"`
	Title     string            `json:"title,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
}

// Options for Create.
type Options struct {
	Path     string
	ID       string // binds the chunks to this recording
	Box      *secret.Box
	Cols     int
	Rows     int
	Title    string
	MaxBytes int64 // output limit; 0 = unlimited
	Now      func() time.Time
}

// Create starts a recording file. The file is created with mode 0600 and
// must not exist yet.
func Create(o Options) (*Recorder, error) {
	if o.Box == nil {
		return nil, errors.New("recording: no master key")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	f, err := os.OpenFile(o.Path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	if _, err := f.WriteString(magic); err != nil {
		f.Close()
		os.Remove(o.Path)
		return nil, err
	}
	r := &Recorder{
		f: f, box: o.Box, id: o.ID, now: o.Now, start: o.Now(), max: o.MaxBytes,
		stop: make(chan struct{}), done: make(chan struct{}),
	}
	h, _ := json.Marshal(Header{
		Version: 2, Width: o.Cols, Height: o.Rows, Timestamp: r.start.Unix(),
		Title: o.Title, Env: map[string]string{"TERM": "xterm-256color"},
	})
	r.buf.Write(h)
	r.buf.WriteByte('\n')
	go r.ticker()
	return r, nil
}

func (r *Recorder) ticker() {
	defer close(r.done)
	t := time.NewTicker(flushEvery)
	defer t.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-t.C:
			r.mu.Lock()
			if !r.closed {
				r.flushLocked()
			}
			r.mu.Unlock()
		}
	}
}

func (r *Recorder) elapsed() string {
	return strconv.FormatFloat(r.now().Sub(r.start).Seconds(), 'f', 6, 64)
}

// event appends one asciicast event line.
func (r *Recorder) event(code, data string) {
	s, _ := json.Marshal(data) // invalid UTF-8 becomes U+FFFD
	r.buf.WriteByte('[')
	r.buf.WriteString(r.elapsed())
	r.buf.WriteString(`, "`)
	r.buf.WriteString(code)
	r.buf.WriteString(`", `)
	r.buf.Write(s)
	r.buf.WriteString("]\n")
	if r.buf.Len() >= chunkSize {
		r.flushLocked()
	}
}

// utf8Cut returns how much of p is complete UTF-8: a multi-byte character
// split across two reads is held back until its tail arrives.
func utf8Cut(p []byte) int {
	for i := len(p) - 1; i >= 0 && i >= len(p)-utf8.UTFMax; i-- {
		b := p[i]
		if b&0xC0 == 0x80 {
			continue // continuation byte
		}
		need := 1
		switch {
		case b&0xE0 == 0xC0:
			need = 2
		case b&0xF0 == 0xE0:
			need = 3
		case b&0xF8 == 0xF0:
			need = 4
		}
		if len(p)-i < need {
			return i
		}
		return len(p)
	}
	return len(p)
}

// Output records bytes the terminal printed.
func (r *Recorder) Output(p []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.stopped || len(p) == 0 {
		return
	}
	data := append(r.partial, p...)
	cut := utf8Cut(data)
	r.partial = append([]byte(nil), data[cut:]...)
	data = data[:cut]
	if len(data) == 0 {
		return
	}
	if r.max > 0 && r.bytes+int64(len(data)) > r.max {
		if room := r.max - r.bytes; room > 0 {
			data = data[:utf8Cut(data[:room])]
			r.bytes += int64(len(data))
			r.event("o", string(data))
		}
		r.event("o", "\r\n\x1b[33m[srvmon: recording stopped, size limit reached]\x1b[0m\r\n")
		r.stopped = true
		return
	}
	r.bytes += int64(len(data))
	r.event("o", string(data))
}

// Resize records a window size change.
func (r *Recorder) Resize(cols, rows int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.stopped {
		return
	}
	r.event("r", fmt.Sprintf("%dx%d", cols, rows))
}

func (r *Recorder) flushLocked() {
	if r.buf.Len() == 0 || r.err != nil {
		return
	}
	sealed, err := r.box.Seal(r.buf.Bytes(), aad(r.id, r.n))
	if err == nil {
		var hdr [4]byte
		binary.BigEndian.PutUint32(hdr[:], uint32(len(sealed)))
		if _, err = r.f.Write(hdr[:]); err == nil {
			_, err = r.f.Write(sealed)
		}
	}
	if err != nil {
		// Disk full or similar: stop recording and drop the buffer, so a
		// busy session does not pile its output up in memory instead.
		r.err = err
		r.stopped = true
		r.buf = bytes.Buffer{}
		r.partial = nil
		return
	}
	r.n++
	r.buf.Reset()
}

// Close flushes the rest and closes the file. It is safe to call twice.
func (r *Recorder) Close() (Stats, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return r.stats(), r.err
	}
	if len(r.partial) > 0 && !r.stopped {
		r.event("o", string(r.partial))
		r.partial = nil
	}
	r.flushLocked()
	r.closed = true
	if err := r.f.Close(); err != nil && r.err == nil {
		r.err = err
	}
	st := r.stats()
	r.mu.Unlock()
	close(r.stop)
	<-r.done
	return st, r.err
}

func (r *Recorder) stats() Stats {
	return Stats{Bytes: r.bytes, Duration: r.now().Sub(r.start), Truncated: r.stopped}
}

func aad(id string, n int) []byte { return []byte("srvmon-rec:" + id + ":" + strconv.Itoa(n)) }

// Open decrypts a recording file into the asciicast text stream.
func Open(path, id string, box *secret.Box) (io.ReadCloser, error) {
	if box == nil {
		return nil, errors.New("recording: no master key")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	br := bufio.NewReader(f)
	m := make([]byte, len(magic))
	if _, err := io.ReadFull(br, m); err != nil || string(m) != magic {
		f.Close()
		return nil, errors.New("recording: not a srvmon recording")
	}
	return &reader{f: f, br: br, box: box, id: id}, nil
}

type reader struct {
	f   *os.File
	br  *bufio.Reader
	box *secret.Box
	id  string
	n   int
	cur []byte
	err error
}

func (r *reader) Read(p []byte) (int, error) {
	for len(r.cur) == 0 {
		if r.err != nil {
			return 0, r.err
		}
		r.next()
	}
	k := copy(p, r.cur)
	r.cur = r.cur[k:]
	return k, nil
}

func (r *reader) next() {
	var hdr [4]byte
	if _, err := io.ReadFull(r.br, hdr[:]); err != nil {
		r.err = io.EOF // clean end, or a header cut by a crash
		return
	}
	size := binary.BigEndian.Uint32(hdr[:])
	if size == 0 || size > maxFrame {
		r.err = errors.New("recording: corrupt frame")
		return
	}
	buf := make([]byte, size)
	if _, err := io.ReadFull(r.br, buf); err != nil {
		r.err = io.EOF // last frame cut by a crash
		return
	}
	pt, err := r.box.Open(buf, aad(r.id, r.n))
	if err != nil {
		r.err = fmt.Errorf("recording: chunk %d: %w", r.n, err)
		return
	}
	r.n++
	r.cur = pt
}

func (r *reader) Close() error { return r.f.Close() }
