package main

import (
	"context"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"winnow/internal/model"
)

// Regression tests for the silent-SDR wedge: rtl_tcp stays alive but delivers
// no samples, rtlamr produces no lines, and the old ingest blocked forever on
// its scanner — the pipeline looked "up" while capturing nothing until the
// container was restarted by hand.

type memSink struct {
	mu       sync.Mutex
	readings int
	hbs      int
}

func (s *memSink) InsertReading(ctx context.Context, r model.Reading, raw string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readings++
	return nil
}
func (s *memSink) UpdateHeartbeat(ctx context.Context, source string, lastTS time.Time, total int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hbs++
	return nil
}

func shrinkSilence(t *testing.T, d time.Duration) {
	t.Helper()
	old := captureSilenceTimeout
	captureSilenceTimeout = d
	t.Cleanup(func() { captureSilenceTimeout = old })
}

func amrLine(id int, cons float64) string {
	return fmt.Sprintf(`{"Time":"2026-07-05T00:00:00Z","Type":"SCM","Message":{"EndpointID":%d,"Consumption":%.0f}}`, id, cons)
}

func TestIngestStallsOnSilence(t *testing.T) {
	shrinkSilence(t, 300*time.Millisecond)
	r, w := io.Pipe()
	t.Cleanup(func() { w.Close() })
	go func() {
		for i := 1; i <= 3; i++ {
			fmt.Fprintln(w, amrLine(100+i, 500))
		}
		// then: total silence, pipe stays open — the wedge
	}()

	start := time.Now()
	stalled, n := ingest(context.Background(), &memSink{}, "t", r)
	if !stalled {
		t.Fatal("a silent-but-open pipe must be declared stalled")
	}
	if n != 3 {
		t.Fatalf("expected 3 packets before the stall, got %d", n)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("stall detection took %s — watchdog not bounding the read", elapsed)
	}
}

func TestIngestEOFIsNotAStall(t *testing.T) {
	shrinkSilence(t, time.Hour)
	r, w := io.Pipe()
	go func() {
		fmt.Fprintln(w, amrLine(101, 500))
		fmt.Fprintln(w, amrLine(102, 600))
		w.Close() // subprocess exited — the already-handled restart path
	}()

	stalled, n := ingest(context.Background(), &memSink{}, "t", r)
	if stalled {
		t.Fatal("EOF is a subprocess exit, not a stall")
	}
	if n != 2 {
		t.Fatalf("expected 2 packets, got %d", n)
	}
}

func TestIngestLinesResetTheIdleWindow(t *testing.T) {
	shrinkSilence(t, 300*time.Millisecond)
	r, w := io.Pipe()
	go func() {
		// keep lines flowing well past one idle window, spaced under it
		for i := 0; i < 8; i++ {
			fmt.Fprintln(w, amrLine(200+i, 700))
			time.Sleep(100 * time.Millisecond)
		}
		w.Close()
	}()

	stalled, n := ingest(context.Background(), &memSink{}, "t", r)
	if stalled {
		t.Fatal("a steadily-producing pipe stalled — per-line timer reset is broken")
	}
	if n != 8 {
		t.Fatalf("expected 8 packets, got %d", n)
	}
}

func TestDeadRunEscalation(t *testing.T) {
	// two dead cycles: keep retrying in-process
	if noteDeadRun("a", true, 0) || noteDeadRun("a", true, 0) {
		t.Fatal("escalated before the third consecutive dead run")
	}
	// third consecutive zero-packet stall: give up (container restart)
	if !noteDeadRun("a", true, 0) {
		t.Fatal("third consecutive dead run must escalate")
	}

	// any packets — even on a run that later stalled — reset the counter
	noteDeadRun("b", true, 0)
	noteDeadRun("b", true, 0)
	if noteDeadRun("b", true, 12) {
		t.Fatal("a run that ingested packets must reset the escalation counter")
	}
	if noteDeadRun("b", true, 0) || noteDeadRun("b", true, 0) {
		t.Fatal("counter did not restart cleanly after a productive run")
	}
	// clean exits (EOF restarts) never escalate
	for i := 0; i < 10; i++ {
		if noteDeadRun("c", false, 0) {
			t.Fatal("non-stall exits must never escalate")
		}
	}
	// sources count independently
	noteDeadRun("d", true, 0)
	noteDeadRun("d", true, 0)
	if noteDeadRun("e", true, 0) {
		t.Fatal("dead runs must be counted per source")
	}
}
