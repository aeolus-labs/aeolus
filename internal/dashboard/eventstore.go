package dashboard

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// eventStore appends one JSON-encoded Event per line to a file on disk
// so the dashboard's recent-calls view survives daemon restarts.
//
// Design notes:
//   - JSONL because it's grep/tail-friendly, easy to back up, and works
//     fine for the dashboard's "show me the last N calls" use case.
//   - All writes serialized through a mutex — tool calls produce one
//     event each and we don't see contention worth optimizing.
//   - Soft size cap: when the file exceeds maxBytes, we rewrite it with
//     just the tail. Cheap because the file is bounded.
//   - All failure modes log + continue: the dashboard must keep working
//     even if disk is full or read-only.
type eventStore struct {
	path     string
	maxBytes int64
	keep     int // events to retain on compaction
	log      *slog.Logger

	mu          sync.Mutex
	file        *os.File
	bytes       int64
	writesSince int
}

const (
	defaultEventStoreMaxBytes = 10 << 20 // 10 MiB
	defaultEventStoreKeep     = 2000     // events retained after compaction
	compactionCheckEvery      = 200      // run compaction check every N writes
)

// openEventStore opens or creates the events file at path and prepares
// it for appends. The file is created with mode 0o600 so secrets that
// leak into args/responses don't end up world-readable.
func openEventStore(path string, log *slog.Logger) (*eventStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create events dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open events file: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("stat events file: %w", err)
	}
	return &eventStore{
		path:     path,
		maxBytes: defaultEventStoreMaxBytes,
		keep:     defaultEventStoreKeep,
		log:      log,
		file:     f,
		bytes:    info.Size(),
	}, nil
}

// Append serializes e and writes it as one line. Any IO error is logged
// and dropped — losing one event is preferable to taking down the
// daemon.
func (s *eventStore) Append(e Event) {
	if s == nil {
		return
	}
	line, err := json.Marshal(e)
	if err != nil {
		s.log.Error("event_store_marshal_failed", "error", err.Error())
		return
	}
	line = append(line, '\n')

	s.mu.Lock()
	n, err := s.file.Write(line)
	if err != nil {
		s.mu.Unlock()
		s.log.Error("event_store_write_failed", "error", err.Error())
		return
	}
	s.bytes += int64(n)
	s.writesSince++
	shouldCompact := s.writesSince >= compactionCheckEvery && s.bytes > s.maxBytes
	if shouldCompact {
		s.writesSince = 0
	}
	s.mu.Unlock()

	if shouldCompact {
		go s.compact()
	}
}

// LoadTail returns the last max events from the file in chronological
// order (oldest first). Missing or corrupt files yield an empty slice.
func (s *eventStore) LoadTail(max int) []Event {
	if s == nil || max <= 0 {
		return nil
	}
	f, err := os.Open(s.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			s.log.Error("event_store_open_failed", "error", err.Error())
		}
		return nil
	}
	defer f.Close()

	// Ring buffer of the last `max` lines as raw bytes — avoids holding
	// the whole file in memory if it's near the soft cap.
	ring := make([][]byte, max)
	head := 0
	count := 0

	scanner := bufio.NewScanner(f)
	// Some args/responses can be up to ~16 KiB plus the envelope;
	// 64 KiB scanner buffer covers that comfortably.
	scanner.Buffer(make([]byte, 0, 64<<10), 256<<10)
	for scanner.Scan() {
		ring[head] = append(ring[head][:0], scanner.Bytes()...)
		head = (head + 1) % max
		if count < max {
			count++
		}
	}
	if err := scanner.Err(); err != nil {
		s.log.Error("event_store_scan_failed", "error", err.Error())
	}

	out := make([]Event, 0, count)
	start := (head - count + max) % max
	for i := 0; i < count; i++ {
		idx := (start + i) % max
		var e Event
		if err := json.Unmarshal(ring[idx], &e); err != nil {
			continue
		}
		out = append(out, e)
	}
	return out
}

// Close flushes and closes the underlying file. Safe to call on nil.
func (s *eventStore) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	return err
}

// compact rewrites the events file with only the most recent `keep`
// lines. The write goes to a sibling tmp file and is renamed in to
// avoid losing data if the rewrite is interrupted.
func (s *eventStore) compact() {
	tail := s.LoadTail(s.keep)
	if len(tail) == 0 {
		return
	}

	tmpPath := s.path + ".tmp"
	tmp, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		s.log.Error("event_store_compact_open_failed", "error", err.Error())
		return
	}
	w := bufio.NewWriter(tmp)
	for _, e := range tail {
		b, err := json.Marshal(e)
		if err != nil {
			continue
		}
		if _, err := w.Write(append(b, '\n')); err != nil {
			s.log.Error("event_store_compact_write_failed", "error", err.Error())
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
			return
		}
	}
	if err := w.Flush(); err != nil {
		s.log.Error("event_store_compact_flush_failed", "error", err.Error())
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return
	}
	if err := tmp.Close(); err != nil {
		s.log.Error("event_store_compact_close_failed", "error", err.Error())
		_ = os.Remove(tmpPath)
		return
	}

	// Swap files under the lock so concurrent Appends don't write into
	// the file we're about to replace.
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.file != nil {
		_ = s.file.Close()
		s.file = nil
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		s.log.Error("event_store_compact_rename_failed", "error", err.Error())
		// Try to reopen the original file so we don't lose Appends.
		s.file, _ = os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		return
	}

	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		s.log.Error("event_store_compact_reopen_failed", "error", err.Error())
		return
	}
	s.file = f
	if info, err := f.Stat(); err == nil {
		s.bytes = info.Size()
	}
	s.log.Info("event_store_compacted", "kept_events", len(tail), "bytes", s.bytes)
}

