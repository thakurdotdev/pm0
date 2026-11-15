//go:build linux

// StreamLogs (M3 shape): the backlog comes from the app's logbus ring
// (bounded by the I8 budget: lines + bytes), and the live follow phase is
// a push subscription on the same route — no more 250ms file polling for
// routed apps. Apps without a route (no sinks configured) fall back to
// the M2 file tail. The wire contract: backlog first, live lines after;
// backlog lines arrive in write order (stdout and stderr interleaved,
// apps merged by arrival time) so the newest line is always last.
//
// Attach discipline (no gaps, no duplicates): the subscription is created
// BEFORE the backlog snapshot; both carry the route's monotonic entry
// sequence. The forwarder drops live entries whose Seq is covered by the
// snapshot, so every line is delivered exactly once across the attach.
//
// Offset discipline (fallback path): only COMPLETE lines advance the
// read offset. A trailing partial line stays unconsumed and is re-read
// (and sent once) when its newline arrives — no data loss across polls.
//
// Drop markers (divergence 9 / I8): a StreamLogs consumer that falls
// behind its route's subscription buffer receives a
// "[pm0: N <stream> lines dropped]" marker line before the next
// delivered line of that stream — loss is announced, never silent.
package daemon

import (
	"context"
	"io"
	"os"
	"sync"
	"time"

	v1 "github.com/pm0/pm0/api/v1"
	"github.com/pm0/pm0/internal/logbus"
	"github.com/pm0/pm0/internal/supervisor"
)

const (
	logPollInterval = 250 * time.Millisecond // fallback file poll only
	maxBacklogBytes = 256 << 10              // read at most the last 256 KiB for backlog
)

// StreamLogs implements the server stream: backlog (when lines > 0),
// then live follow (when follow is set). Ends when the client leaves or
// every routed app's route closes (app deleted mid-stream).
func (s *Server) StreamLogs(req *v1.StreamLogsRequest, stream v1.Daemon_StreamLogsServer) error {
	views := s.resolveSelector(req.GetSelector())
	stderrWanted := req.GetIncludeStderr()
	n := int(req.GetLines())

	type bound struct {
		view   supervisor.AppView
		route  *logbus.Route // nil => file fallback
		sub    *logbus.Sub   // live subscription (created before the backlog snapshot)
		maxSeq uint64        // highest entry Seq covered by the backlog phase
	}
	bounds := make([]bound, 0, len(views))
	for _, v := range views {
		b := bound{view: v, route: s.ensureRoute(v)}
		if b.route != nil {
			b.sub = b.route.Subscribe() // BEFORE the snapshot: no attach window
		}
		bounds = append(bounds, b)
	}
	defer func() {
		for i := range bounds {
			if bounds[i].sub != nil {
				bounds[i].sub.Close()
			}
		}
	}()

	type offsetKey struct {
		id     int
		stderr bool
	}
	fbOffsets := make(map[offsetKey]int64) // fallback offsets seeded by the backlog phase

	// Backlog phase: up to n lines per stream per app, delivered in the
	// order they were written — NOT all stdout then all stderr, which
	// buried the latest stdout under older stderr (a chatty-erroring app
	// showed its newest success lines halfway up the screen). Within an
	// app the route's monotonic Seq is the arrival order across both
	// streams; across apps (`pm0 logs` with no name) entries are merged by
	// arrival time so the newest line of any app prints last.
	if n > 0 {
		var routed [][]logLineEntry
		for i := range bounds {
			b := &bounds[i]
			if b.route != nil {
				all := b.route.BacklogAll(stderrWanted)
				for _, e := range all {
					if e.Seq > b.maxSeq {
						b.maxSeq = e.Seq
					}
				}
				routed = append(routed, backlogTail(b.view, all, n, stderrWanted))
				continue
			}
			// File fallback (no sinks => no route; M2 behavior).
			if lines, off, ok := tailFile(b.view.Config.OutFile, n); ok {
				for _, line := range lines {
					if err := stream.Send(logLine(b.view, false, line)); err != nil {
						return err
					}
				}
				fbOffsets[offsetKey{id: b.view.ID}] = off
			}
			if stderrWanted {
				if lines, off, ok := tailFile(b.view.Config.ErrorFile, n); ok {
					for _, line := range lines {
						if err := stream.Send(logLine(b.view, true, line)); err != nil {
							return err
						}
					}
					fbOffsets[offsetKey{id: b.view.ID, stderr: true}] = off
				}
			}
		}
		for _, l := range mergeByArrival(routed) {
			if err := stream.Send(l.line); err != nil {
				return err
			}
		}
	}

	if !req.GetFollow() {
		return nil
	}

	// Follow phase: per-app forwarders feed one merged channel; a single
	// sender goroutine owns the stream (grpc streams are single-writer).
	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()
	merged := make(chan *v1.LogLine, 256)
	var wg sync.WaitGroup

	for i := range bounds {
		b := &bounds[i]
		if b.route != nil {
			maxSeq := b.maxSeq // snapshot; forwarder-local
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-ctx.Done():
						return
					case e, ok := <-b.sub.C():
						if !ok {
							return // route closed (app deleted mid-stream)
						}
						if e.Dropped == 0 && e.Seq <= maxSeq {
							continue // already delivered by the backlog phase
						}
						if e.Stream == logbus.Stderr && !stderrWanted {
							continue // separate sinks: err filtered on request
						}
						select {
						case <-ctx.Done():
							return
						case merged <- logLine(b.view, e.Stream == logbus.Stderr, e.Data):
						}
					}
				}
			}()
			continue
		}
		// File-fallback forwarder (M2 poll loop, per view).
		wg.Add(1)
		go func() {
			defer wg.Done()
			t := time.NewTicker(logPollInterval)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					// Merged fallback (out == err path): one pass only,
					// tagged stdout — err passes would double-send.
					sinks := []struct {
						path   string
						stderr bool
					}{{b.view.Config.OutFile, false}}
					if b.view.Config.ErrorFile != b.view.Config.OutFile {
						sinks = append(sinks, struct {
							path   string
							stderr bool
						}{b.view.Config.ErrorFile, true})
					}
					for _, sink := range sinks {
						if sink.stderr && !stderrWanted {
							continue // err file disabled for this stream
						}
						key := offsetKey{id: b.view.ID, stderr: sink.stderr}
						off := fbOffsets[key]
						noff, lines, err := readNewLines(sink.path, off)
						if err != nil {
							continue // sink not created yet
						}
						fbOffsets[key] = noff
						for _, line := range lines {
							select {
							case <-ctx.Done():
								return
							case merged <- logLine(b.view, sink.stderr, line):
							}
						}
					}
				}
			}
		}()
	}

	forwardersDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(forwardersDone)
	}()

	for {
		select {
		case <-ctx.Done():
			return nil // client left; forwarders wind down on the same ctx
		case <-forwardersDone:
			return nil // every route closed; nothing more can arrive
		case line := <-merged:
			if err := stream.Send(line); err != nil {
				return err
			}
		}
	}
}

// logLineEntry is one backlog line plus its arrival time (merge key).
type logLineEntry struct {
	line *v1.LogLine
	at   time.Time
}

// backlogTail keeps the last n entries of each stream from one app's ring
// snapshot (oldest first, Seq order) and returns them still in Seq order,
// i.e. interleaved exactly as the app wrote them.
func backlogTail(v supervisor.AppView, all []logbus.Entry, n int, stderrWanted bool) []logLineEntry {
	var outLeft, errLeft = n, n
	if !stderrWanted {
		errLeft = 0
	}
	keep := make([]bool, len(all))
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].Stream == logbus.Stderr {
			if errLeft > 0 {
				keep[i] = true
				errLeft--
			}
		} else if outLeft > 0 {
			keep[i] = true
			outLeft--
		}
	}
	out := make([]logLineEntry, 0, 2*n)
	for i, e := range all {
		if keep[i] {
			out = append(out, logLineEntry{line: logLine(v, e.Stream == logbus.Stderr, e.Data), at: e.At})
		}
	}
	return out
}

// mergeByArrival k-way merges per-app backlogs by arrival time. Each input
// keeps its own order (Seq, never re-sorted: two lines from one poll share
// a timestamp); ties across apps go to the earlier app in selector order.
func mergeByArrival(apps [][]logLineEntry) []logLineEntry {
	total := 0
	for _, a := range apps {
		total += len(a)
	}
	out := make([]logLineEntry, 0, total)
	heads := make([]int, len(apps))
	for len(out) < total {
		best := -1
		for i, a := range apps {
			if heads[i] >= len(a) {
				continue
			}
			if best < 0 || a[heads[i]].at.Before(apps[best][heads[best]].at) {
				best = i
			}
		}
		out = append(out, apps[best][heads[best]])
		heads[best]++
	}
	return out
}

func logLine(v supervisor.AppView, stderr bool, data string) *v1.LogLine {
	st := v1.LogLine_STREAM_STDOUT
	if stderr {
		st = v1.LogLine_STREAM_STDERR
	}
	return &v1.LogLine{
		PmId:   int32(v.ID),
		Name:   v.Config.Name,
		Stream: st,
		Data:   []byte(data),
	}
}

// tailFile returns the last n complete lines of path and the byte offset
// just after them (the follow start point). A trailing partial line is
// NOT emitted and NOT counted — the follow phase picks it up when it
// completes. ok=false when the file cannot be read (never created yet).
func tailFile(path string, n int) ([]string, int64, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, 0, false
	}
	size := st.Size()
	start := int64(0)
	if size > maxBacklogBytes {
		start = size - maxBacklogBytes
	}
	buf, err := io.ReadAll(io.NewSectionReader(f, start, size-start))
	if err != nil {
		return nil, 0, false
	}
	lines, partial := splitComplete(buf)
	if start > 0 && len(lines) > 0 {
		lines = lines[1:] // first line may be partial (seeked into the middle)
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	// Offset covers everything up to the end of the last COMPLETE line;
	// a trailing partial stays unconsumed so follow re-reads it whole.
	off := start + int64(len(buf)-len(partial))
	return lines, off, true
}

// readNewLines reads complete lines appended after off. On truncation
// (rotation/rewrite) it restarts from zero. Returns the new offset
// (complete lines only) and the lines seen.
func readNewLines(path string, off int64) (int64, []string, error) {
	f, err := os.Open(path)
	if err != nil {
		return off, nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return off, nil, err
	}
	if st.Size() < off {
		off = 0 // truncated/rotated: start over
	}
	if st.Size() == off {
		return off, nil, nil
	}
	buf, err := io.ReadAll(io.NewSectionReader(f, off, st.Size()-off))
	if err != nil {
		return off, nil, err
	}
	lines, _ := splitComplete(buf)
	consumed := 0
	for _, l := range lines {
		consumed += len(l) + 1 // newline
	}
	return off + int64(consumed), lines, nil
}

// splitComplete splits buf into complete (newline-terminated) lines; the
// trailing partial chunk, if any, is returned separately and callers keep
// it unconsumed.
func splitComplete(buf []byte) (lines []string, partial []byte) {
	start := 0
	for i, b := range buf {
		if b == '\n' {
			lines = append(lines, string(buf[start:i]))
			start = i + 1
		}
	}
	if start < len(buf) {
		partial = buf[start:]
	}
	return lines, partial
}
