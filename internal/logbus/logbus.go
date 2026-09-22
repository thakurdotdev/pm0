// Package logbus is the M3 log router: per-app capture of the on-disk
// log sinks (compat rows 26/27) into a memory-bounded ring buffer with
// live fan-out to stream subscribers, plus built-in size-based rotation.
//
// Capture model: child stdio keeps going to the append-mode log files
// (proc.Spec wiring, M2). The bus pumps TAIL those files — the same
// complete-line offset discipline as the M2 StreamLogs poller — so the
// capture is transparent to the child, survives adoption (adopted trees
// write the same sinks) and never interferes with the kill paths.
//
// Bounds (invariant I8: never silent loss, memory bounded by budget):
//
//   - ring: bounded by BOTH a line count and a cumulative byte budget;
//     overflow evicts oldest lines (counted, OnDrop fires);
//   - lines longer than MaxLineBytes are truncated in the ring only
//     (counted; the FILE keeps the full line);
//   - each subscriber owns a bounded channel; a slow subscriber's lines
//     are dropped COUNTED and a "[pm0: N lines dropped]" marker is
//     inserted into that subscriber's stream before the next delivered
//     line of the same stream (divergence 9 — PM2 drops silently here).
//
// Rotation: size-based, copytruncate style (logrotate's approach for
// uncooperative writers — copy the file aside, then ftruncate). Children
// keep their O_APPEND fds and never need to cooperate; a microsecond
// window between the final read and the truncate can lose in-flight
// bytes (documented in divergence 12). Rotated names carry a timestamp;
// retain caps how many rotated files per sink survive.
package logbus

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// Stream tags a captured line's origin.
type Stream int

const (
	// Stdout marks lines captured from the out sink (row 26).
	Stdout Stream = iota
	// Stderr marks lines captured from the err sink (row 27).
	Stderr
)

// Label renders the stream for drop markers.
func (s Stream) Label() string {
	if s == Stderr {
		return "stderr"
	}
	return "stdout"
}

// Entry is one captured log line. Seq is the route-wide monotonic
// sequence assigned under the route lock: consumers coordinate backlog
// snapshots and live feeds on it (no gaps, no duplicates across the
// attach boundary).
type Entry struct {
	Stream Stream
	At     time.Time
	Data   string
	Seq    uint64
}

// Defaults applied when Options fields are zero. Ring bounds follow the
// I8 budget rule: worst-case ring memory per app is RingLines *
// MaxLineBytes, while typical text logs land near the byte budget.
const (
	DefaultRingLines    = 1000
	DefaultRingBytes    = 1 << 20 // 1 MiB cumulative budget
	DefaultMaxLineBytes = 8 << 10 // 8 KiB per line (ring-side truncation)
	DefaultSubBuffer    = 1024    // per-subscriber channel lines
	DefaultRotateMax    = 10 << 20
	DefaultRotateRetain = 30
	DefaultPollInterval = 100 * time.Millisecond

	// maxIdlePoll caps the pump's idle backoff interval: a sink with no
	// new bytes is re-checked at most once per second (one statx), which
	// is what makes a large quiet fleet cost ~0 CPU.
	maxIdlePoll = time.Second
	// fastPollWindow: silence lasting this long before the interval
	// starts widening — one full second at the default 100ms cadence.
	fastPollWindow = time.Second

	// maxBatchBytes caps one pump read pass. With rotation on (the
	// default) a batch is bounded by the rotate threshold anyway; with
	// rotation OFF and the daemon tick-starved (SIGSTOP, host pause,
	// cgroup freeze), the sink could have grown gigabytes between two
	// ticks — a single read of that would balloon the daemon's heap for
	// data no consumer can observe (the ring holds ~1MiB, a subscriber
	// channel ~1024 lines; everything older is self-evicting). Past the
	// cap the head is skipped COUNTED (I8: never silent loss) and the
	// freshest maxBatchBytes are delivered; the log FILES remain the
	// full-history authority.
	maxBatchBytes = 64 << 20

	dropMarkerFormat = "[pm0: %d %s lines dropped]"
)

// Options configures one Route. Zero fields take the defaults above.
type Options struct {
	OutPath string // stdout sink (row 26); empty disables the out pump
	ErrPath string // stderr sink (row 27); empty disables the err pump
	// Equal non-empty paths realize merge_logs (row 29): one pump tails
	// the shared file and ring entries are tagged Stdout (the merged
	// stream has no per-stream identity on disk).
	RingLines      int
	RingBytes      int64
	MaxLineBytes   int
	SubBuffer      int
	RotateMaxBytes int64 // 0 => rotation disabled
	RotateRetain   int
	PollInterval   time.Duration
	// OnDrop is called synchronously on eviction/truncation/subscriber
	// drops (rate limiting is the caller's concern). May be nil.
	OnDrop func(kind string, stream Stream, n int)
}

// Stats is a point-in-time snapshot of the counters that make I8
// observable: nothing is ever lost silently.
type Stats struct {
	Evicted    uint64 // lines evicted from the ring by overflow
	Truncated  uint64 // lines truncated to MaxLineBytes (ring only)
	Rotations  uint64 // rotation events across both sinks
	SubsActive int
}

// Route is one app's log bus. Create with NewRoute, then Close when the
// app is deleted. Safe for concurrent use.
type Route struct {
	opts Options

	mu        sync.Mutex
	ring      []Entry // fixed capacity; head = oldest live entry
	head      int
	count     int
	ringLen   int64  // cumulative Data bytes currently in the ring
	seq       uint64 // last assigned entry sequence (route-wide)
	evicted   uint64
	truncated uint64
	rotations uint64
	subs      map[*Sub]struct{}
	closed    bool

	quit chan struct{}
	wake chan struct{} // subscriber-attach signal: pumps snap back to base cadence
	wg   sync.WaitGroup
}

// Sub is one live consumer of a Route. Subscribe returns it; the C
// channel yields entries until the route (or consumer) closes.
type Sub struct {
	ch      chan Entry
	route   *Route
	drops   uint64
	pending map[Stream]int // undelivered count per stream; a marker precedes the next delivery
}

// NewRoute builds and starts the pumps for one app's sinks.
func NewRoute(opts Options) *Route {
	if opts.RingLines <= 0 {
		opts.RingLines = DefaultRingLines
	}
	if opts.RingBytes <= 0 {
		opts.RingBytes = DefaultRingBytes
	}
	if opts.MaxLineBytes <= 0 {
		opts.MaxLineBytes = DefaultMaxLineBytes
	}
	if opts.SubBuffer <= 0 {
		opts.SubBuffer = DefaultSubBuffer
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = DefaultPollInterval
	}
	if opts.RotateRetain <= 0 {
		opts.RotateRetain = DefaultRotateRetain
	}
	r := &Route{
		opts: opts,
		ring: make([]Entry, opts.RingLines),
		subs: make(map[*Sub]struct{}),
		quit: make(chan struct{}),
		wake: make(chan struct{}, 1),
	}
	// Merged sink: exactly one pump; the shared file is read once and
	// every line is tagged Stdout (see Options comment).
	if r.merged() {
		r.wg.Add(1)
		go r.pump(opts.OutPath, Stdout)
	} else {
		if opts.OutPath != "" {
			r.wg.Add(1)
			go r.pump(opts.OutPath, Stdout)
		}
		if opts.ErrPath != "" {
			r.wg.Add(1)
			go r.pump(opts.ErrPath, Stderr)
		}
	}
	return r
}

func (r *Route) merged() bool {
	return r.opts.OutPath != "" && r.opts.OutPath == r.opts.ErrPath
}

// Subscribe registers a live consumer. The returned channel closes when
// the route closes. Lines are dropped COUNTED when the consumer falls
// behind (I8 / divergence 9); a drop marker precedes the next delivery
// on the same stream.
func (r *Route) Subscribe() *Sub {
	s := &Sub{
		ch:      make(chan Entry, r.opts.SubBuffer),
		route:   r,
		pending: make(map[Stream]int),
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		close(s.ch)
		return s
	}
	r.subs[s] = struct{}{}
	r.mu.Unlock()
	// Nudge idle pumps back to the base cadence so the new subscriber
	// sees live lines promptly (non-blocking; data arrival snaps pumps
	// back anyway — this only shortens the first line's wait).
	select {
	case r.wake <- struct{}{}:
	default:
	}
	return s
}

// C yields live entries until the route closes.
func (s *Sub) C() <-chan Entry { return s.ch }

// Close releases the subscriber early (stream consumers leaving before
// the route closes). Idempotent.
func (s *Sub) Close() { s.route.unsubscribe(s) }

// Drops reports how many lines were dropped for this subscriber.
func (s *Sub) Drops() uint64 {
	s.route.mu.Lock()
	defer s.route.mu.Unlock()
	return s.drops
}

// unsubscribe removes a subscriber and closes its channel. No send can
// race the close: fanout holds the same lock.
func (r *Route) unsubscribe(s *Sub) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.subs[s]; !ok {
		return
	}
	delete(r.subs, s)
	close(s.ch)
}

// Backlog returns up to n most recent entries, oldest first, filtered to
// the requested streams. Fewer entries are returned when the ring holds
// less (evicted history is gone; the FILES remain the full-history
// authority). With includeStderr=false only Stdout-tagged entries are
// returned — a merged sink therefore keeps flowing to stdout-only
// consumers, matching pm2's merge_logs rendering.
func (r *Route) Backlog(n int, includeStderr bool) []Entry {
	if n <= 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Entry, 0, min(n, r.count))
	for i := 0; i < r.count; i++ {
		e := r.ring[(r.head+i)%len(r.ring)]
		if e.Stream == Stderr && !includeStderr && !r.merged() {
			continue
		}
		out = append(out, e)
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

// BacklogAll returns every entry currently in the ring, oldest first,
// filtered to the requested streams. StreamLogs splits this per stream
// to honor the M2 wire shape (up to n lines per stream, out before err).
func (r *Route) BacklogAll(includeStderr bool) []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Entry, 0, r.count)
	for i := 0; i < r.count; i++ {
		e := r.ring[(r.head+i)%len(r.ring)]
		if e.Stream == Stderr && !includeStderr && !r.merged() {
			continue
		}
		out = append(out, e)
	}
	return out
}

// Stats snapshots the I8 counters.
func (r *Route) Stats() Stats {
	r.mu.Lock()
	defer r.mu.Unlock()
	return Stats{
		Evicted:    r.evicted,
		Truncated:  r.truncated,
		Rotations:  r.rotations,
		SubsActive: len(r.subs),
	}
}

// Clear empties the in-memory ring buffer.
func (r *Route) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ring = make([]Entry, r.opts.RingLines)
	r.head = 0
	r.count = 0
	r.ringLen = 0
}

// Close stops the pumps and closes every subscriber channel. The log
// FILES are untouched (pm2 keeps them on delete too).
func (r *Route) Close() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	close(r.quit)
	subs := make([]*Sub, 0, len(r.subs))
	for s := range r.subs {
		subs = append(subs, s)
	}
	r.subs = make(map[*Sub]struct{})
	r.mu.Unlock()
	for _, s := range subs {
		close(s.ch)
	}
	r.wg.Wait()
}

// append fans captured lines into the ring and the subscribers. Called
// by the pumps only. Lines share one batch timestamp: per-line time.Now
// at flood rates measured 14% of daemon CPU for sub-microsecond distinct
// values no consumer can observe apart from (100ms poll ticks are the
// real arrival granularity anyway).
func (r *Route) appendLines(lines []string, st Stream, at time.Time) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	evicted := 0
	for i := range lines {
		data := lines[i]
		if len(data) > r.opts.MaxLineBytes {
			data = data[:r.opts.MaxLineBytes] + "...[truncated]"
			r.truncated++
			r.notify("truncated", st, 1)
		}
		r.seq++
		e := Entry{Stream: st, At: at, Data: data, Seq: r.seq}
		evicted += r.pushRing(e)
		if len(r.subs) > 0 {
			r.fanout(e)
		}
	}
	// One aggregated eviction report per batch instead of one per line:
	// at flood rates a 10MB batch can evict ~10^5 lines, and 10^5
	// synchronous callbacks measured 13% of daemon CPU even after the
	// callback went lock-free. The I8 contract (announced, counted,
	// bounded) is n-preserving: the batch total equals the sum of the
	// per-line counts.
	if evicted > 0 {
		r.notify("evicted", st, evicted)
	}
	r.mu.Unlock()
}

// pushRing inserts one entry under both bounds (line count and byte
// budget), evicting oldest-first; every eviction is counted (I8).
// Returns the number of lines evicted (the caller aggregates the
// OnDrop reports per batch — see appendLines).
func (r *Route) pushRing(e Entry) int {
	evicted := 0
	cap := len(r.ring)
	if r.count == cap {
		old := r.ring[r.head]
		r.ringLen -= int64(len(old.Data))
		r.head = (r.head + 1) % cap
		r.count--
		r.evicted++
		evicted++
	}
	for r.count > 0 && r.ringLen+int64(len(e.Data)) > r.opts.RingBytes {
		old := r.ring[r.head]
		r.ringLen -= int64(len(old.Data))
		r.head = (r.head + 1) % cap
		r.count--
		r.evicted++
		evicted++
	}
	tail := (r.head + r.count) % cap
	r.ring[tail] = e
	r.ringLen += int64(len(e.Data))
	r.count++
	return evicted
}

// fanout delivers one entry to every subscriber, counting and staging
// drop markers for the ones that fell behind (divergence 9).
func (r *Route) fanout(e Entry) {
	for s := range r.subs {
		// Opportunistic marker flush: before this stream's next real
		// line, report the gap. Skipped while the channel is still full.
		if n := s.pending[e.Stream]; n > 0 {
			marker := Entry{
				Stream: e.Stream,
				At:     time.Now(),
				Data:   fmt.Sprintf(dropMarkerFormat, n, e.Stream.Label()),
			}
			select {
			case s.ch <- marker:
				s.pending[e.Stream] = 0
			default:
			}
		}
		select {
		case s.ch <- e:
		default:
			s.pending[e.Stream]++
			s.drops++
			r.notify("subscriber", e.Stream, 1)
		}
	}
}

func (r *Route) notify(kind string, st Stream, n int) {
	if r.opts.OnDrop != nil {
		r.opts.OnDrop(kind, st, n)
	}
}

// pump tails one sink until the route closes. Offset discipline matches
// the M2 file poller: only complete (newline-terminated) lines advance
// the offset, a trailing partial line is re-read when its newline
// arrives.
//
// Idle economics (perf queue: idle CPU scaled ~0.03%/app from 2 pumps ×
// 10Hz × open/stat/close per tick at 500 apps): each tick costs one
// os.Stat; the open only happens when the size moved (new bytes,
// truncation, or a fresh file). After a run of empty ticks the interval
// doubles from PollInterval up to maxIdlePoll (1s), and any new data or
// subscriber snaps it back. An active flood never leaves the base
// cadence, so rotation granularity and streaming latency are unchanged
// exactly when they matter; a quiet fleet of hundreds of apps idles at
// one cheap statx per pump-second.
func (r *Route) pump(path string, st Stream) {
	defer r.wg.Done()
	base := r.opts.PollInterval
	interval := base
	tick := time.NewTicker(interval)
	defer tick.Stop()
	var offset int64
	idleTicks := 0
	// Reused read buffer + line slice: at a 100MB/s flood every base
	// cadence tick reads ~10MB — io.ReadAll's grow-from-512B doubling
	// re-allocated (memclr + memmove) ~14 times per tick, which showed
	// as 11% memclr + GC churn in the profile. The buffer grows to the
	// steady-state batch size once and is reused for the pump's life.
	var buf []byte

	backoff := func() {
		idleTicks++
		if interval >= maxIdlePoll {
			return
		}
		if next := interval * 2; next > maxIdlePoll {
			interval = maxIdlePoll
		} else if time.Duration(idleTicks)*interval >= fastPollWindow {
			// only start widening after a full fast-poll window of silence
			interval = next
		} else {
			return
		}
		tick.Reset(interval)
	}
	snapBack := func() {
		idleTicks = 0
		if interval == base {
			return
		}
		interval = base
		tick.Reset(base)
	}

	for {
		select {
		case <-r.quit:
			return
		case <-r.wake:
			// A subscriber attached: deliver live lines at base cadence.
			snapBack()
			continue
		case <-tick.C:
		}
		// One stat drives both the rotation check and the open decision.
		stt, err := os.Stat(path)
		if err != nil {
			offset = 0 // vanished (not created yet, or rotated away externally)
			backoff()
			continue
		}
		if stt.Size() < offset {
			offset = 0 // truncated/rewritten externally
			if stt.Size() == 0 {
				r.Clear()
			}
		}
		if r.opts.RotateMaxBytes > 0 && stt.Size() >= r.opts.RotateMaxBytes {
			if r.rotateIfDue(path) {
				offset = 0
				snapBack()
			}
		}
		if stt.Size() == offset {
			// Fully drained and no growth since the last look.
			backoff()
			continue
		}
		if skip := stt.Size() - offset; skip > maxBatchBytes {
			// Counted head-skip past the batch cap (see the
			// maxBatchBytes contract): report in whole MiB so n
			// stays a sane int even for multi-TB growth.
			r.notify("skipped", st, int(skip>>20))
			offset = stt.Size() - maxBatchBytes
		}
		snapBack()
		var noff int64
		var lines []string
		var rerr error
		noff, lines, buf, rerr = readNewInto(path, offset, buf)
		if rerr != nil {
			offset = 0 // vanished between the stat and the open
			continue
		}
		offset = noff
		if len(lines) > 0 {
			r.appendLines(lines, st, time.Now())
		}
	}
}

// rotateIfDue performs one copytruncate rotation when the sink exceeds
// RotateMaxBytes, then enforces the retain cap. Returns true when the
// file was truncated (the pump's offset resets to zero).
func (r *Route) rotateIfDue(path string) bool {
	if r.opts.RotateMaxBytes <= 0 {
		return false
	}
	st, err := os.Stat(path)
	if err != nil || st.Size() < r.opts.RotateMaxBytes {
		return false
	}
	if err := CopyTruncate(path); err != nil {
		return false // keep tailing; retry next tick
	}
	r.mu.Lock()
	r.rotations++
	r.mu.Unlock()
	PruneRotated(path, r.opts.RotateRetain)
	return true
}

// CopyTruncate copies path to path.<timestamp> and truncates the
// original in place. Writers holding O_APPEND fds (the child) continue
// on the same inode with no cooperation needed. The read loop re-checks
// growth to shrink the loss window to the final truncate.
func CopyTruncate(path string) error {
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer src.Close()
	dstPath := path + "." + time.Now().Format("20060102T150405.000")
	dst, err := os.OpenFile(dstPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	// Read until EOF twice: the second pass catches appends that landed
	// between the first EOF and the truncate decision.
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return err
	}
	if err := dst.Close(); err != nil {
		return err
	}
	return os.Truncate(path, 0)
}

// PruneRotated deletes the oldest rotated siblings beyond retain.
// Timestamp suffixes sort chronologically, so a lexical sort suffices.
func PruneRotated(path string, retain int) {
	dir, base := splitPath(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	prefix := base + "."
	var rotated []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, prefix) && len(name) > len(prefix) {
			rotated = append(rotated, name)
		}
	}
	if len(rotated) <= retain {
		return
	}
	sort.Strings(rotated)
	for _, name := range rotated[:len(rotated)-retain] {
		_ = os.Remove(dir + string(os.PathSeparator) + name)
	}
}

// ReadNewComplete reads complete lines appended after off (the M2
// offset discipline). On truncation/rewrite (size < off) it restarts
// from zero. Exported for the daemon's file-fallback path.
func ReadNewComplete(path string, off int64) (int64, []string, error) {
	noff, lines, _, err := readNewInto(path, off, nil)
	return noff, lines, err
}

// readNewInto is ReadNewComplete with caller-reusable storage. buf is
// grown geometrically and returned for the next tick; lines always own
// fresh strings (they outlive the buffer in the ring). A mid-read file
// shrink races to EOF (the child truncated between stat and read): the
// partial bytes are processed, the next tick re-syncs by offset rule.
func readNewInto(path string, off int64, buf []byte) (int64, []string, []byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return off, nil, buf, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return off, nil, buf, err
	}
	if st.Size() < off {
		off = 0 // truncated/rotated: start over
	}
	if st.Size() == off {
		return off, nil, buf, nil
	}
	want := int(st.Size() - off)
	if cap(buf) < want {
		buf = make([]byte, want+want/2) // 1.5x headroom against per-tick realloc
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return off, nil, buf, err
	}
	got := 0
	for got < want {
		n, rerr := f.Read(buf[got:want])
		got += n
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return off, nil, buf, rerr
		}
	}
	lines, _, consumed := SplitCompleteConsumed(buf[:got])
	return off + int64(consumed), lines, buf, nil
}

// SplitComplete splits buf into complete (newline-terminated) lines; the
// trailing partial chunk, if any, is returned separately and callers
// keep it unconsumed. Exported for the daemon's file-fallback path.
func SplitComplete(buf []byte) (lines []string, partial []byte) {
	lines, partial, _ = SplitCompleteConsumed(buf)
	return lines, partial
}

// SplitCompleteConsumed is SplitComplete that also reports how many
// input bytes the complete lines occupy (sum of len(line)+1), so the
// offset advance needs no third pass over the data. Scanning uses
// bytes.IndexByte (memchr-asm, tens of bytes per cycle) instead of the
// previous per-byte Go loop — the byte loop was a measurable slice of
// flood CPU at 100MB/s.
//
// Consecutive duplicate lines share one string: log floods are highly
// repetitive, and at ~10^5 lines/batch the per-line string allocation
// dominated the pump profile. Sharing is safe (strings are immutable;
// ring entries only read Data) and lossless — every entry still carries
// its own Seq and stream.
func SplitCompleteConsumed(buf []byte) (lines []string, partial []byte, consumed int) {
	count := bytes.Count(buf, []byte{'\n'})
	lines = make([]string, 0, count)
	start := 0
	lastStart, lastLen := -1, 0
	for {
		i := bytes.IndexByte(buf[start:], '\n')
		if i < 0 {
			break
		}
		if lastLen == i && lastLen > 0 && bytes.Equal(buf[start:start+i], buf[lastStart:lastStart+lastLen]) {
			lines = append(lines, lines[len(lines)-1])
		} else {
			lines = append(lines, string(buf[start:start+i]))
			lastStart, lastLen = start, i
		}
		start += i + 1
	}
	if start < len(buf) {
		partial = buf[start:]
	}
	return lines, partial, start
}

func splitPath(path string) (dir, base string) {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[:i], path[i+1:]
	}
	return ".", path
}
