// Package client is the Go SDK for the pm0 daemon: a gRPC client over
// the ~/.pm0/pm0.sock Unix socket plus the PM2-compatible JSON and
// table renderers (docs/compat.md §2: scripts that parse `pm2 jlist` must
// work unchanged against `pm0 jlist`).
package client

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	v1 "github.com/pm0/pm0/api/v1"
	"github.com/pm0/pm0/internal/store"
)

// DefaultSocketPath is the control socket (compat divergence 1).
func DefaultSocketPath() string { return "unix://" + store.SocketPath() }

// Client talks to the daemon. Methods map 1:1 onto the Daemon service.
type Client struct {
	conn   *grpc.ClientConn
	Daemon v1.DaemonClient
}

// Dial connects to the daemon socket (or an explicit target like
// "unix:///path/sock").
func Dial(target string) (*Client, error) {
	if target == "" {
		target = DefaultSocketPath()
	}
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("client: dial %s: %w", target, err)
	}
	return &Client{conn: conn, Daemon: v1.NewDaemonClient(conn)}, nil
}

// Close tears the connection down.
func (c *Client) Close() error { return c.conn.Close() }

func (c *Client) ctx(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

// Ping probes liveness and returns daemon metadata.
func (c *Client) Ping() (*v1.PingResponse, error) {
	ctx, cancel := c.ctx(2 * time.Second)
	defer cancel()
	return c.Daemon.Ping(ctx, &v1.PingRequest{})
}

// Start starts apps from specs.
func (c *Client) Start(specs []*v1.ProcessSpec) (*v1.StartProcessResponse, error) {
	ctx, cancel := c.ctx(30 * time.Second)
	defer cancel()
	return c.Daemon.StartProcess(ctx, &v1.StartProcessRequest{Specs: specs})
}

// Stop stops selected apps.
func (c *Client) Stop(sel *v1.Selector) (*v1.StopProcessResponse, error) {
	return c.StopN(sel, 1)
}

// StopN stops selected apps with up to n concurrent stop RPCs.
// n<=1 keeps the legacy sequential behavior (staggered downtime, ordered
// ids); n>1 fans out one RPC per selected app ("all" is resolved to
// per-id selectors via List first, so every RPC carries an explicit id).
// The concurrency lives in the CLI fan-out only: the daemon still stops
// each app's tree through the actor mailbox, so per-app semantics
// (SIGINT -> kill_timeout -> SIGKILL) are unchanged.
func (c *Client) StopN(sel *v1.Selector, n int) (*v1.StopProcessResponse, error) {
	sels := c.selectorsForParallel(sel, n)
	if len(sels) == 1 {
		return c.stopOne(sels[0], 30*time.Second)
	}
	out := make([]stopRes, len(sels))
	var wg sync.WaitGroup
	for i, s := range sels {
		wg.Add(1)
		go func(i int, s *v1.Selector) {
			defer wg.Done()
			r, err := c.stopOne(s, 60*time.Second)
			out[i] = stopRes{r, err}
		}(i, s)
	}
	wg.Wait()
	return mergeStop(out)
}

func (c *Client) stopOne(sel *v1.Selector, d time.Duration) (*v1.StopProcessResponse, error) {
	ctx, cancel := c.ctx(d)
	defer cancel()
	return c.Daemon.StopProcess(ctx, &v1.StopProcessRequest{Selector: sel})
}

// Restart restarts selected apps (updatedSpec carries env for --update-env).
func (c *Client) Restart(sel *v1.Selector, updatedSpec *v1.ProcessSpec) (*v1.RestartProcessResponse, error) {
	return c.RestartN(sel, updatedSpec, 1)
}

// RestartN restarts selected apps with up to n concurrent restart RPCs.
// n<=1 keeps the legacy sequential behavior (staggered downtime, ordered
// ids); n>1 fans out one RPC per selected app. Each restart is still
// stop+start per app through the actor mailbox, so per-app semantics are
// unchanged. Reload is intentionally NOT parallelized here: rolling
// replacements must stay sequential to keep the port/rollback contract.
func (c *Client) RestartN(sel *v1.Selector, updatedSpec *v1.ProcessSpec, n int) (*v1.RestartProcessResponse, error) {
	sels := c.selectorsForParallel(sel, n)
	if len(sels) == 1 {
		return c.restartOne(sels[0], updatedSpec, 60*time.Second)
	}
	out := make([]restartRes, len(sels))
	var wg sync.WaitGroup
	for i, s := range sels {
		wg.Add(1)
		go func(i int, s *v1.Selector) {
			defer wg.Done()
			r, err := c.restartOne(s, updatedSpec, 120*time.Second)
			out[i] = restartRes{r, err}
		}(i, s)
	}
	wg.Wait()
	return mergeRestart(out)
}

func (c *Client) restartOne(sel *v1.Selector, updatedSpec *v1.ProcessSpec, d time.Duration) (*v1.RestartProcessResponse, error) {
	ctx, cancel := c.ctx(d)
	defer cancel()
	return c.Daemon.RestartProcess(ctx, &v1.RestartProcessRequest{Selector: sel, UpdatedSpec: updatedSpec})
}

// Delete stops and forgets selected apps.
func (c *Client) Delete(sel *v1.Selector) (*v1.DeleteProcessResponse, error) {
	return c.DeleteN(sel, 1)
}

// DeleteN deletes selected apps with up to n concurrent delete RPCs.
// n<=1 keeps the legacy sequential behavior; n>1 fans out one RPC per
// selected app (each delete stops the tree then frees the pm_id).
func (c *Client) DeleteN(sel *v1.Selector, n int) (*v1.DeleteProcessResponse, error) {
	sels := c.selectorsForParallel(sel, n)
	if len(sels) == 1 {
		return c.deleteOne(sels[0], 30*time.Second)
	}
	out := make([]deleteRes, len(sels))
	var wg sync.WaitGroup
	for i, s := range sels {
		wg.Add(1)
		go func(i int, s *v1.Selector) {
			defer wg.Done()
			r, err := c.deleteOne(s, 60*time.Second)
			out[i] = deleteRes{r, err}
		}(i, s)
	}
	wg.Wait()
	return mergeDelete(out)
}

func (c *Client) deleteOne(sel *v1.Selector, d time.Duration) (*v1.DeleteProcessResponse, error) {
	ctx, cancel := c.ctx(d)
	defer cancel()
	return c.Daemon.DeleteProcess(ctx, &v1.DeleteProcessRequest{Selector: sel})
}

// splitSelector fans a multi-app selector out into per-app selectors so the
// caller can issue one RPC per app concurrently. n<=1 (or a selector that
// is already a single app) returns the selector unchanged; otherwise the
// fan-out is bounded to n selectors. Multi-id/name selectors are split
// into singles with the tail packed into one trailing selector so at most
// n RPCs run concurrently. "all" (or empty/nil) is NOT fanned out here —
// the caller resolves it to concrete ids first via List
// (see selectorsForParallel), so every RPC carries an explicit id and
// results stay attributable.
func splitSelector(sel *v1.Selector, n int) []*v1.Selector {
	if sel == nil || n <= 1 || (!sel.GetAll() && len(sel.GetPmIds())+len(sel.GetNames()) <= 1) {
		return []*v1.Selector{sel}
	}
	if sel.GetAll() {
		return []*v1.Selector{sel}
	}
	var singles []*v1.Selector
	for _, id := range sel.GetPmIds() {
		singles = append(singles, &v1.Selector{PmIds: []int32{id}})
	}
	for _, name := range sel.GetNames() {
		singles = append(singles, &v1.Selector{Names: []string{name}})
	}
	if len(singles) == 0 {
		return []*v1.Selector{sel}
	}
	if n > 0 && len(singles) > n {
		kept := singles[:n-1]
		var tailIDs []int32
		var tailNames []string
		for _, s := range singles[n-1:] {
			tailIDs = append(tailIDs, s.GetPmIds()...)
			tailNames = append(tailNames, s.GetNames()...)
		}
		kept = append(kept, &v1.Selector{PmIds: tailIDs, Names: tailNames})
		return kept
	}
	return singles
}

// selectorsForParallel returns the per-RPC selectors for a parallel
// fan-out: explicit per-app selectors stay as splitSelector gives them;
// "all" (or empty/nil) is resolved to per-id selectors via List so each
// RPC carries an explicit id — results stay attributable and AffectedPmIds
// can be re-sorted deterministically. Falls back to the single selector on
// empty list or List error (server then runs it sequentially as before).
func (c *Client) selectorsForParallel(sel *v1.Selector, n int) []*v1.Selector {
	if sel != nil && !sel.GetAll() && len(sel.GetPmIds())+len(sel.GetNames()) > 0 {
		return splitSelector(sel, n)
	}
	resp, err := c.List()
	if err != nil {
		return []*v1.Selector{sel}
	}
	procs := resp.GetProcesses()
	if len(procs) == 0 {
		return []*v1.Selector{sel}
	}
	ids := make([]int32, 0, len(procs))
	for _, p := range procs {
		ids = append(ids, p.GetPmId())
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var out []*v1.Selector
	for _, id := range ids {
		out = append(out, &v1.Selector{PmIds: []int32{id}})
	}
	if n > 0 && len(out) > n {
		kept := out[:n-1]
		var tail []int32
		for _, s := range out[n-1:] {
			tail = append(tail, s.GetPmIds()...)
		}
		kept = append(kept, &v1.Selector{PmIds: tail})
		return kept
	}
	return out
}

func mergeIDs(sets ...[]int32) []int32 {
	seen := map[int32]bool{}
	var out []int32
	for _, set := range sets {
		for _, id := range set {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

type stopRes struct {
	resp *v1.StopProcessResponse
	err  error
}

type restartRes struct {
	resp *v1.RestartProcessResponse
	err  error
}

type deleteRes struct {
	resp *v1.DeleteProcessResponse
	err  error
}

func mergeStop(out []stopRes) (*v1.StopProcessResponse, error) {
	merged := &v1.StopProcessResponse{}
	var firstErr error
	for _, r := range out {
		if r.err != nil {
			if firstErr == nil {
				firstErr = r.err
			}
			continue
		}
		if r.resp != nil {
			merged.AffectedPmIds = mergeIDs(merged.AffectedPmIds, r.resp.GetAffectedPmIds())
		}
	}
	if len(merged.AffectedPmIds) == 0 && firstErr != nil {
		return merged, firstErr
	}
	return merged, firstErr
}

func mergeRestart(out []restartRes) (*v1.RestartProcessResponse, error) {
	merged := &v1.RestartProcessResponse{}
	var firstErr error
	for _, r := range out {
		if r.err != nil {
			if firstErr == nil {
				firstErr = r.err
			}
			continue
		}
		if r.resp != nil {
			merged.AffectedPmIds = mergeIDs(merged.AffectedPmIds, r.resp.GetAffectedPmIds())
		}
	}
	if len(merged.AffectedPmIds) == 0 && firstErr != nil {
		return merged, firstErr
	}
	return merged, firstErr
}

func mergeDelete(out []deleteRes) (*v1.DeleteProcessResponse, error) {
	merged := &v1.DeleteProcessResponse{}
	var firstErr error
	for _, r := range out {
		if r.err != nil {
			if firstErr == nil {
				firstErr = r.err
			}
			continue
		}
		if r.resp != nil {
			merged.AffectedPmIds = mergeIDs(merged.AffectedPmIds, r.resp.GetAffectedPmIds())
		}
	}
	if len(merged.AffectedPmIds) == 0 && firstErr != nil {
		return merged, firstErr
	}
	return merged, firstErr
}

// Reload rolling-restarts selected apps.
func (c *Client) Reload(sel *v1.Selector) (*v1.ReloadProcessResponse, error) {
	ctx, cancel := c.ctx(120 * time.Second)
	defer cancel()
	return c.Daemon.ReloadProcess(ctx, &v1.ReloadProcessRequest{Selector: sel})
}

// Scale adjusts an app's instance count.
func (c *Client) Scale(name string, delta int32) (*v1.ScaleProcessResponse, error) {
	ctx, cancel := c.ctx(60 * time.Second)
	defer cancel()
	return c.Daemon.ScaleProcess(ctx, &v1.ScaleProcessRequest{Name: name, Delta: delta})
}

// List returns all apps.
func (c *Client) List() (*v1.ListProcessesResponse, error) {
	ctx, cancel := c.ctx(10 * time.Second)
	defer cancel()
	return c.Daemon.ListProcesses(ctx, &v1.ListProcessesRequest{})
}

// Describe returns one app in full.
func (c *Client) Describe(sel *v1.Selector) (*v1.DescribeProcessResponse, error) {
	ctx, cancel := c.ctx(10 * time.Second)
	defer cancel()
	return c.Daemon.DescribeProcess(ctx, &v1.DescribeProcessRequest{Selector: sel})
}

// Save persists the app set.
func (c *Client) Save() (*v1.SaveResponse, error) {
	ctx, cancel := c.ctx(10 * time.Second)
	defer cancel()
	return c.Daemon.Save(ctx, &v1.SaveRequest{})
}

// Resurrect adopts/starts the dumped app set.
func (c *Client) Resurrect() (*v1.ResurrectResponse, error) {
	ctx, cancel := c.ctx(120 * time.Second)
	defer cancel()
	return c.Daemon.Resurrect(ctx, &v1.ResurrectRequest{})
}

// Update re-execs the daemon in place.
func (c *Client) Update() error {
	ctx, cancel := c.ctx(30 * time.Second)
	defer cancel()
	stream, err := c.Daemon.UpdateDaemon(ctx, &v1.UpdateDaemonRequest{})
	if err != nil {
		return err
	}
	for {
		_, err = stream.Recv()
		if err != nil {
			break // the re-exec closes the stream mid-flight — expected
		}
	}
	return nil
}

// Kill stops the daemon (force skips stopping apps first).
func (c *Client) Kill(force bool) (*v1.KillDaemonResponse, error) {
	ctx, cancel := c.ctx(60 * time.Second)
	defer cancel()
	return c.Daemon.KillDaemon(ctx, &v1.KillDaemonRequest{Force: force})
}

// StreamLogs opens the log stream; the returned channel ends when the
// server closes the stream.
//
// End-of-stream contract: the terminal error arrives on errCh BEFORE
// linesCh is closed, so a consumer selecting over both can observe the
// error while buffered lines are still undelivered. Consumers MUST drain
// linesCh after taking the error (guaranteed to terminate: the producer
// closes linesCh immediately after the error send) — exiting on the
// error alone silently truncates the backlog tail.
func (c *Client) StreamLogs(ctx context.Context, sel *v1.Selector, lines int32, includeStderr, raw, follow bool) (<-chan *v1.LogLine, <-chan error, error) {
	stream, err := c.Daemon.StreamLogs(ctx, &v1.StreamLogsRequest{
		Selector:      sel,
		Lines:         lines,
		IncludeStderr: includeStderr,
		Raw:           raw,
		Follow:        follow,
	})
	if err != nil {
		return nil, nil, err
	}
	linesCh := make(chan *v1.LogLine, 64)
	errCh := make(chan error, 1)
	go func() {
		defer close(linesCh)
		for {
			line, err := stream.Recv()
			if err != nil {
				errCh <- err
				return
			}
			linesCh <- line
		}
	}()
	return linesCh, errCh, nil
}
