package logbus

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// fastOpts builds options tuned for tests: tiny bounds, 5ms pumps, no
// rotation unless asked.
func fastOpts(dir string) Options {
	return Options{
		OutPath:        filepath.Join(dir, "app-out.log"),
		ErrPath:        filepath.Join(dir, "app-error.log"),
		RingLines:      8,
		RingBytes:      4096,
		MaxLineBytes:   4096,
		SubBuffer:      64,
		PollInterval:   5 * time.Millisecond,
		RotateMaxBytes: 0,
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

// appendInject drives r.appendLines directly (no pumps: both paths
// empty), for precise counter assertions.
func appendInject(r *Route, st Stream, data string) {
	r.appendLines([]string{data}, st, time.Now())
}

func TestRingOrderCapAndFilters(t *testing.T) {
	r := NewRoute(Options{RingLines: 8, RingBytes: 4096, MaxLineBytes: 4096, SubBuffer: 8})
	defer r.Close()
	for _, s := range []string{"a", "b", "c"} {
		appendInject(r, Stdout, s)
	}
	appendInject(r, Stderr, "E1")

	if got := r.Backlog(10, true); len(got) != 4 || got[0].Data != "a" || got[3].Data != "E1" {
		t.Fatalf("backlog all: %+v", got)
	}
	if got := r.Backlog(10, false); len(got) != 3 || got[2].Data != "c" {
		t.Fatalf("backlog stdout-only: %+v", got)
	}
	if got := r.Backlog(2, true); len(got) != 2 || got[0].Data != "c" || got[1].Data != "E1" {
		t.Fatalf("backlog last-2: %+v", got)
	}
	if got := r.Backlog(0, true); got != nil {
		t.Fatalf("backlog zero: %+v", got)
	}
}

func TestRingEvictionCounts(t *testing.T) {
	r := NewRoute(Options{RingLines: 3, RingBytes: 4096, MaxLineBytes: 4096, SubBuffer: 8})
	defer r.Close()
	for i := 0; i < 5; i++ {
		appendInject(r, Stdout, fmt.Sprint(i))
	}
	got := r.Backlog(10, true)
	if len(got) != 3 || got[0].Data != "2" || got[2].Data != "4" {
		t.Fatalf("ring should hold 2,3,4: %+v", got)
	}
	if r.Stats().Evicted != 2 {
		t.Fatalf("evicted = %d, want 2", r.Stats().Evicted)
	}
}

func TestRingByteBudget(t *testing.T) {
	// Budget 100 bytes; each line 15 bytes incl. newline accounting in
	// Data length only (15 * no newline here). 7 lines fit under 100
	// only after evictions: budget must never be exceeded.
	r := NewRoute(Options{RingLines: 100, RingBytes: 100, MaxLineBytes: 4096, SubBuffer: 8})
	defer r.Close()
	for i := 0; i < 20; i++ {
		appendInject(r, Stdout, strings.Repeat("x", 15))
	}
	st := r.Stats()
	if st.Evicted == 0 {
		t.Fatal("byte budget should force evictions")
	}
	r.mu.Lock()
	ringLen := r.ringLen
	count := r.count
	r.mu.Unlock()
	if ringLen > 100 {
		t.Fatalf("ring holds %d bytes, budget 100", ringLen)
	}
	if count == 0 {
		t.Fatal("newest line must survive even over budget")
	}
}

func TestRingLineTruncation(t *testing.T) {
	r := NewRoute(Options{RingLines: 8, RingBytes: 4096, MaxLineBytes: 16, SubBuffer: 8})
	defer r.Close()
	appendInject(r, Stdout, strings.Repeat("y", 100))
	got := r.Backlog(1, true)
	if len(got) != 1 {
		t.Fatalf("backlog: %+v", got)
	}
	want := strings.Repeat("y", 16) + "...[truncated]"
	if got[0].Data != want {
		t.Fatalf("truncated line = %q, want %q", got[0].Data, want)
	}
	if r.Stats().Truncated != 1 {
		t.Fatalf("truncated counter = %d", r.Stats().Truncated)
	}
}

func TestSlowSubscriberDropMarker(t *testing.T) {
	r := NewRoute(Options{RingLines: 8, RingBytes: 4096, MaxLineBytes: 4096, SubBuffer: 2})
	defer r.Close()
	s := r.Subscribe()

	for i := 0; i < 10; i++ {
		appendInject(r, Stdout, fmt.Sprint(i))
	}
	if s.Drops() != 8 {
		t.Fatalf("drops = %d, want 8 (buffer 2)", s.Drops())
	}

	// Drain the two buffered lines.
	<-s.C()
	<-s.C()

	// Next append flushes the marker for the 8 dropped lines first.
	appendInject(r, Stdout, "after")
	marker := <-s.C()
	if !strings.Contains(marker.Data, "[pm0: 8 stdout lines dropped]") {
		t.Fatalf("marker = %q", marker.Data)
	}
	line := <-s.C()
	if line.Data != "after" {
		t.Fatalf("post-marker line = %q", line.Data)
	}

	// Stderr drops do not disturb the stdout marker stream: pending is
	// tracked per stream.
	appendInject(r, Stderr, "boom")
	e := <-s.C()
	if e.Data != "boom" || e.Stream != Stderr {
		t.Fatalf("stderr line = %+v", e)
	}
}

func TestFilePumpCapture(t *testing.T) {
	dir := t.TempDir()
	r := NewRoute(fastOpts(dir))
	defer r.Close()

	out := fastOpts(dir).OutPath
	errp := fastOpts(dir).ErrPath

	// Partial line: not captured until its newline arrives.
	mustAppend(t, out, "partial")
	time.Sleep(30 * time.Millisecond)
	if got := r.Backlog(10, true); len(got) != 0 {
		t.Fatalf("partial line captured: %+v", got)
	}
	mustAppend(t, out, "-rest\n") // completes the physical line "partial-rest"
	mustAppend(t, out, "out-1\n")
	mustAppend(t, errp, "err-1\n")
	waitFor(t, "3 captured lines", func() bool { return len(r.Backlog(10, true)) == 3 })

	// Two independent pumps append in nondeterministic interleaving, so
	// only per-stream relative order is guaranteed.
	all := r.Backlog(10, true)
	if len(all) != 3 {
		t.Fatalf("backlog = %+v", all)
	}
	var outLines []string
	var errLines []string
	for _, e := range all {
		if e.Stream == Stdout {
			outLines = append(outLines, e.Data)
		} else {
			errLines = append(errLines, e.Data)
		}
	}
	if len(outLines) != 2 || outLines[0] != "partial-rest" || outLines[1] != "out-1" {
		t.Fatalf("out stream order = %+v", outLines)
	}
	if len(errLines) != 1 || errLines[0] != "err-1" {
		t.Fatalf("err stream order = %+v", errLines)
	}
	// Stderr filtered for stdout-only consumers.
	if got := r.Backlog(10, false); len(got) != 2 {
		t.Fatalf("stdout-only backlog = %+v", got)
	}
}

func TestMergedSink(t *testing.T) {
	dir := t.TempDir()
	opts := fastOpts(dir)
	opts.ErrPath = opts.OutPath // merge_logs (row 29)
	r := NewRoute(opts)
	defer r.Close()

	mustAppend(t, opts.OutPath, "merged-1\nmerged-2\n")
	waitFor(t, "2 merged lines", func() bool { return len(r.Backlog(10, true)) == 2 })

	got := r.Backlog(10, false) // stdout-only consumers see everything merged
	if len(got) != 2 || got[0].Data != "merged-1" || got[1].Data != "merged-2" {
		t.Fatalf("merged backlog = %+v", got)
	}
	for _, e := range got {
		if e.Stream != Stdout {
			t.Fatalf("merged entry tagged %v", e.Stream)
		}
	}
	if r.Stats().SubsActive != 0 {
		// sanity: stats plumbing alive
		t.Fatal("unexpected active subs")
	}
}

func TestRotationCopyTruncateAndRetain(t *testing.T) {
	dir := t.TempDir()
	opts := fastOpts(dir)
	opts.RotateMaxBytes = 300
	opts.RotateRetain = 2
	r := NewRoute(opts)
	defer r.Close()

	out := opts.OutPath
	line := strings.Repeat("L", 19) + "\n" // 20 bytes
	for i := 0; i < 25; i++ {              // 500 bytes > 300: one rotation
		mustAppend(t, out, line)
	}
	waitFor(t, "first rotation", func() bool { return r.Stats().Rotations >= 1 })

	// The live file keeps receiving (child's O_APPEND fd untouched).
	mustAppend(t, out, "after-rotation\n")
	waitFor(t, "post-rotation capture", func() bool {
		for _, e := range r.Backlog(50, true) {
			if e.Data == "after-rotation" {
				return true
			}
		}
		return false
	})

	// Rotated copies hold the pre-rotation history.
	rot := rotatedFiles(t, dir, "app-out.log.")
	if len(rot) == 0 {
		t.Fatal("no rotated files")
	}
	if st, err := os.Stat(filepath.Join(dir, rot[0])); err != nil || st.Size() == 0 {
		t.Fatal("rotated file empty or unreadable")
	}

	// Force enough rotations to exercise the retain cap (keep 2).
	for round := 0; round < 5; round++ {
		for i := 0; i < 20; i++ {
			mustAppend(t, out, line)
		}
		waitFor(t, fmt.Sprintf("rotation round %d", round), func() bool {
			return len(rotatedFiles(t, dir, "app-out.log.")) > round+1 || r.Stats().Rotations >= uint64(round+2)
		})
		t.Logf("round %d: rotations=%d rotated=%v live=%d", round,
			r.Stats().Rotations, rotatedFiles(t, dir, "app-out.log."), liveSize(t, out))
	}
	waitFor(t, "retain cap", func() bool {
		return len(rotatedFiles(t, dir, "app-out.log.")) <= 2
	})
	if n := len(rotatedFiles(t, dir, "app-out.log.")); n > 2 {
		t.Fatalf("retain violated: %d rotated files", n)
	}
}

func TestCloseStopsPumpsAndClosesSubs(t *testing.T) {
	dir := t.TempDir()
	before := runtime.NumGoroutine()
	r := NewRoute(fastOpts(dir))
	s := r.Subscribe()
	r.Close()

	// Subscriber channel must be closed by Close.
	select {
	case _, ok := <-s.C():
		if ok {
			t.Fatal("expected closed subscriber channel")
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber channel not closed")
	}
	// Second Close is idempotent.
	r.Close()

	waitFor(t, "pump goroutines exit", func() bool {
		runtime.Gosched()
		return runtime.NumGoroutine() <= before+1
	})

	// Backlog remains readable after close; append is a no-op.
	if got := r.Backlog(5, true); len(got) != 0 {
		t.Fatalf("post-close backlog = %+v", got)
	}
	appendInject(r, Stdout, "ignored")
}

func TestReadNewCompleteOffsetDiscipline(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.log")

	if _, _, err := ReadNewComplete(p, 0); err == nil {
		t.Fatal("missing file should error")
	}
	mustAppend(t, p, "one\ntwo\npar")
	off, lines, err := ReadNewComplete(p, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0] != "one" || lines[1] != "two" {
		t.Fatalf("lines = %+v", lines)
	}
	if off != int64(len("one\ntwo\n")) {
		t.Fatalf("offset = %d: partial must stay unconsumed", off)
	}
	off2, lines2, _ := ReadNewComplete(p, off)
	if len(lines2) != 0 {
		t.Fatalf("partial re-read produced lines: %+v", lines2)
	}
	mustAppend(t, p, "tial\n")
	off3, lines3, _ := ReadNewComplete(p, off2)
	if len(lines3) != 1 || lines3[0] != "partial" {
		t.Fatalf("completed partial = %+v", lines3)
	}
	if off3 != int64(len("one\ntwo\npartial\n")) {
		t.Fatalf("final offset = %d", off3)
	}
}

func TestConcurrentAccess(t *testing.T) {
	dir := t.TempDir()
	r := NewRoute(fastOpts(dir))
	defer r.Close()

	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() { // writer thread
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			mustAppend(t, fastOpts(dir).OutPath, fmt.Sprintf("line-%d\n", i))
			time.Sleep(time.Millisecond)
		}
	}()
	wg.Add(1)
	go func() { // churn subscribers
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			s := r.Subscribe()
			<-s.C() // at least close arrives even if no line does
			s.Close()
		}
	}()
	for i := 0; i < 50; i++ {
		_ = r.Backlog(3, i%2 == 0)
		_ = r.Stats()
		time.Sleep(time.Millisecond)
	}
	close(stop)
	wg.Wait()
}

// Sub.Close releases the subscriber without waiting for the route close.
func TestSubExplicitClose(t *testing.T) {
	r := NewRoute(Options{RingLines: 8, RingBytes: 4096, MaxLineBytes: 4096, SubBuffer: 4})
	s := r.Subscribe()
	s.Close()
	select {
	case _, ok := <-s.C():
		if ok {
			t.Fatal("expected closed channel")
		}
	default:
		t.Fatal("channel should be closed after Sub.Close")
	}
	// Double close is a no-op.
	s.Close()
	r.Close()
}

func mustAppend(t *testing.T, path, data string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(data); err != nil {
		t.Fatal(err)
	}
}

func rotatedFiles(t *testing.T, dir, prefix string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) {
			out = append(out, e.Name())
		}
	}
	return out
}

func liveSize(t *testing.T, path string) int64 {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		return -1
	}
	return st.Size()
}

func TestRouteClear(t *testing.T) {
	r := NewRoute(Options{
		RingLines: 10,
		RingBytes: 1024,
	})
	defer r.Close()

	r.appendLines([]string{"line1", "line2"}, Stdout, time.Now())
	if len(r.BacklogAll(true)) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(r.BacklogAll(true)))
	}

	r.Clear()
	if len(r.BacklogAll(true)) != 0 {
		t.Fatalf("expected 0 lines after Clear, got %d", len(r.BacklogAll(true)))
	}
}

