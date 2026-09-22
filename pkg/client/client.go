// Package client is the Go SDK for the pm0 daemon: a gRPC client over
// the ~/.pm0/pm0.sock Unix socket plus the PM2-compatible JSON and
// table renderers (docs/compat.md §2: scripts that parse `pm2 jlist` must
// work unchanged against `pm0 jlist`).
package client

import (
	"context"
	"fmt"
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
	ctx, cancel := c.ctx(30 * time.Second)
	defer cancel()
	return c.Daemon.StopProcess(ctx, &v1.StopProcessRequest{Selector: sel})
}

// Restart restarts selected apps (updatedSpec carries env for --update-env).
func (c *Client) Restart(sel *v1.Selector, updatedSpec *v1.ProcessSpec) (*v1.RestartProcessResponse, error) {
	ctx, cancel := c.ctx(60 * time.Second)
	defer cancel()
	return c.Daemon.RestartProcess(ctx, &v1.RestartProcessRequest{Selector: sel, UpdatedSpec: updatedSpec})
}

// Delete stops and forgets selected apps.
func (c *Client) Delete(sel *v1.Selector) (*v1.DeleteProcessResponse, error) {
	ctx, cancel := c.ctx(30 * time.Second)
	defer cancel()
	return c.Daemon.DeleteProcess(ctx, &v1.DeleteProcessRequest{Selector: sel})
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
