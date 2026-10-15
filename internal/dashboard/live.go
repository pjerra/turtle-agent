package dashboard

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"sync"
	"time"
)

// Live follows the trace of each agent's current run and keeps its digest, so that a request for the map's path layers costs a
// copy of numbers and no parsing. A poll reads only the bytes the trace has gained; the file is only ever opened for reading.
const (
	liveChunk = 8 << 20 // the most one advance reads
	liveMax   = 48 << 20
	liveIdle  = 10 * time.Minute // a file nobody advanced for this long is dropped
	// MaxTrailMinutes bounds the minutes a page may ask for.
	MaxTrailMinutes = 120
)

type liveFile struct {
	offset int64
	b      *digestBuilder
	seen   time.Time
	ver    uint64
	cache  map[int][]byte
}

// Live is the set of followed traces.
type Live struct {
	mu    sync.Mutex
	files map[string]*liveFile
}

// NewLive makes an empty set.
func NewLive() *Live { return &Live{files: map[string]*liveFile{}} }

// Advance brings the digest of the trace at path up to date. The caller has already checked the path.
func (l *Live) Advance(path string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for p, f := range l.files {
		if now.Sub(f.seen) > liveIdle {
			delete(l.files, p)
		}
	}
	f := l.files[path]
	if f == nil {
		f = &liveFile{b: newDigestBuilder(), cache: map[int][]byte{}}
		l.files[path] = f
	}
	f.seen = now
	fh, err := os.Open(path)
	if err != nil {
		return
	}
	defer fh.Close()
	st, err := fh.Stat()
	if err != nil {
		return
	}
	if st.Size() < f.offset { // replaced or cut: start over
		f.offset, f.b, f.cache = 0, newDigestBuilder(), map[int][]byte{}
	}
	if f.offset >= st.Size() || f.offset >= liveMax {
		return
	}
	if _, err := fh.Seek(f.offset, io.SeekStart); err != nil {
		return
	}
	data, _ := io.ReadAll(io.LimitReader(fh, liveChunk))
	last := bytes.LastIndexByte(data, '\n')
	if last < 0 {
		return
	}
	data = data[:last+1]
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		if i > 0 && i <= maxLine {
			f.b.Line(data[:i])
		}
		data = data[i+1:]
	}
	f.offset += int64(last + 1)
}

// JSON is the digest of the trace at path as JSON with the walks cut to the last minutes (0: whole), cached until the trace grows.
// ok is false for a trace nobody advanced.
func (l *Live) JSON(path string, minutes int) ([]byte, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f := l.files[path]
	if f == nil || f.b.run == "" {
		return nil, false
	}
	if minutes < 0 || minutes > MaxTrailMinutes {
		minutes = MaxTrailMinutes
	}
	if f.ver != f.b.version {
		f.ver, f.cache = f.b.version, map[int][]byte{}
	}
	if c, ok := f.cache[minutes]; ok {
		return c, true
	}
	b, err := json.Marshal(f.b.Snapshot(minutes))
	if err != nil {
		return nil, false
	}
	f.cache[minutes] = b
	return b, true
}
