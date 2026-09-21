// Package events records what the run actually did, as ground truth for whoever reads the
// stream back. The e2e suite infers our behaviour from player symptoms today; this replaces
// that inference with a fact.
package events

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"sync"
	"time"
)

// Kinds emitted by the pipeline.
const (
	StreamStart   = "stream.start"
	StreamEnd     = "stream.end"
	Loop          = "loop"
	HoldStart     = "hold.start"
	HoldBurst     = "hold.burst"
	HoldDrop      = "hold.drop"
	JitterBurst   = "jitter.burst"
	FaultInjected = "fault.injected"
	SCTE35Cue     = "scte35.cue"
	MetaSent      = "meta.sent"
)

// Fields are the per-kind extras of an event.
type Fields map[string]any

// Event is one line of the log. The two clocks are always both present: t_media is what a
// scenario schedules against, t_wall is what a reader of the stream sees.
type Event struct {
	Wall   time.Time
	Media  time.Duration
	Kind   string
	Fields Fields
}

func (e Event) MarshalJSON() ([]byte, error) {
	head, err := json.Marshal(struct {
		Wall  string  `json:"t_wall"`
		Media float64 `json:"t_media"`
		Kind  string  `json:"kind"`
	}{e.Wall.UTC().Format(time.RFC3339Nano), e.Media.Seconds(), e.Kind})
	if err != nil || len(e.Fields) == 0 {
		return head, err
	}
	tail, err := json.Marshal(e.Fields)
	if err != nil {
		return nil, err
	}
	return append(append(head[:len(head)-1], ','), tail[1:]...), nil
}

// Log writes events.jsonl and tallies them for the run summary. A nil *Log discards
// everything, so callers never have to check.
type Log struct {
	mu     sync.Mutex
	w      io.Writer
	closer io.Closer
	err    error
	start  time.Time
	counts map[string]int
}

// Open creates the JSONL file. An empty path returns a log that only counts, which is what
// --summary without --events needs.
func Open(path string) (*Log, error) {
	l := &Log{start: time.Now(), counts: map[string]int{}}
	if path == "" {
		return l, nil
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("creating event log: %w", err)
	}
	l.w, l.closer = f, f
	return l, nil
}

// Emit records one event. Errors are kept and reported by Close rather than interrupting a run.
func (l *Log) Emit(kind string, media time.Duration, fields Fields) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	l.counts[kind]++
	if l.w == nil {
		return
	}
	line, err := json.Marshal(Event{Wall: time.Now(), Media: media, Kind: kind, Fields: fields})
	if err == nil {
		_, err = l.w.Write(append(line, '\n'))
	}
	if err != nil && l.err == nil {
		l.err = err
	}
}

// Summary is the run in one object: what was asked for, how long it lasted, what happened.
type Summary struct {
	Source   string         `json:"source"`
	Dest     string         `json:"dest"`
	Format   string         `json:"format"`
	Seed     int64          `json:"seed"`
	Started  string         `json:"started"`
	Wall     float64        `json:"wall_seconds"`
	Media    float64        `json:"media_seconds"`
	Packets  int64          `json:"packets"`
	Bytes    int64          `json:"bytes"`
	Events   map[string]int `json:"events"`
	Outcome  string         `json:"outcome"`
	ExitNote string         `json:"note,omitempty"`
}

// Summarise fills in the parts the log knows about; the caller adds the rest.
func (l *Log) Summarise() Summary {
	s := Summary{Events: map[string]int{}}
	if l == nil {
		return s
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	s.Started = l.start.UTC().Format(time.RFC3339Nano)
	s.Wall = time.Since(l.start).Seconds()
	for k, v := range l.counts {
		s.Events[k] = v
	}
	return s
}

func (l *Log) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closer != nil {
		if err := l.closer.Close(); err != nil && l.err == nil {
			l.err = err
		}
		l.closer, l.w = nil, nil
	}
	return l.err
}

// WriteSummary writes summary.json.
func WriteSummary(path string, s Summary) error {
	if path == "" {
		return nil
	}
	// Keep the map stable so two runs of the same scenario diff cleanly.
	ordered := make(map[string]int, len(s.Events))
	keys := make([]string, 0, len(s.Events))
	for k := range s.Events {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		ordered[k] = s.Events[k]
	}
	s.Events = ordered

	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}
