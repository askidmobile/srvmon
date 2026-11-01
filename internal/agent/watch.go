package agent

import (
	"os"
	"time"
)

// fileWatch notices that a file was replaced (renamed over, as the panel
// does when it updates a systemd agent) or modified in place.
type fileWatch struct {
	files []watched
}

type watched struct {
	path  string
	st    os.FileInfo // nil: the file did not exist at the start
	mtime time.Time
	size  int64
}

func newFileWatch(paths []string) *fileWatch {
	w := &fileWatch{}
	for _, p := range paths {
		if p == "" {
			continue
		}
		f := watched{path: p}
		if st, err := os.Stat(p); err == nil {
			f.st, f.mtime, f.size = st, st.ModTime(), st.Size()
		}
		w.files = append(w.files, f)
	}
	return w
}

// changed returns the first file that is not the one seen at the start,
// or "". A file that disappears does not count: the update renames the new
// file into place, and a missing binary would not start again anyway.
func (w *fileWatch) changed() string {
	if w == nil {
		return ""
	}
	for _, f := range w.files {
		st, err := os.Stat(f.path)
		if err != nil {
			continue
		}
		if f.st == nil || !os.SameFile(f.st, st) || !st.ModTime().Equal(f.mtime) || st.Size() != f.size {
			return f.path
		}
	}
	return ""
}
