//go:build linux

// Package daemon is the pm0 control plane: a gRPC Daemon service on the
// ~/.pm0/pm0.sock Unix socket (0600), wrapping the supervisor registry.
//
// Lifecycle model (docs/compat.md divergence 1 and §3):
//
//   - one daemon per user home; the CLI spawns it detached on first use
//     (`pm0 daemon` runs it in the foreground for debugging);
//   - UpdateDaemon re-execs THIS process in place (same PID): the children
//     survive the exec, the new image re-adopts every live tree by its
//     _PM0_APP attribution marker and resumes supervision;
//   - KillDaemon stops every app (unless --force), unlinks the socket and
//     exits; SIGTERM/SIGINT on the daemon do the same;
//   - resurrect after a crash re-adopts surviving trees or starts fresh,
//     preserving dump pm_ids (§3.2).
package daemon

import (
	"context"
	"fmt"
	"net"
	"net/http"
	_ "net/http/pprof" // opt-in: PM0_PPROF_ADDR serves /debug/pprof on 127.0.0.1
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	v1 "github.com/pm0/pm0/api/v1"
	"github.com/pm0/pm0/internal/cluster"
	"github.com/pm0/pm0/internal/logbus"
	"github.com/pm0/pm0/internal/ops"
	"github.com/pm0/pm0/internal/proc"
	"github.com/pm0/pm0/internal/store"
	"github.com/pm0/pm0/internal/supervisor"
)

// updateFlagEnv marks a re-exec'd daemon image: on boot it re-adopts the
// dumped app set instead of starting empty.
const updateFlagEnv = "_PM0_UPDATE"

// ErrAlreadyRunning is returned when a live daemon owns the socket.
var ErrAlreadyRunning = fmt.Errorf("daemon: another daemon is already running on %s", store.SocketPath())

// Server implements pm0.v1.Daemon.
type Server struct {
	v1.UnimplementedDaemonServer

	sup    *supervisor.Supervisor
	launch *proc.Launcher

	version string
	commit  string

	redactEnv bool

	mu      sync.Mutex
	cpuLast map[int]cpuSample     // pm_id -> last cpu ticks sample (monit)
	routes  map[int]*logbus.Route // pm_id -> log router (M3; guarded by mu)

	// procSnapCache serves shared /proc passes to the monitoring paths
	// (list/monit/describe/treeRSS). One pass per min-age window replaces
	// the per-app scans that made list O(apps × processes).
	procSnapCache *procSnapshotCache

	opsRecon *ops.Reconciler // M5 enforcers (memory/cron/health/watch)
}

// New builds the daemon server. version/commit surface through Ping.
func New(l *proc.Launcher, version, commit string, redactEnv bool) *Server {
	s := &Server{
		sup:           supervisor.New(l),
		launch:        l,
		version:       version,
		commit:        commit,
		redactEnv:     redactEnv,
		cpuLast:       make(map[int]cpuSample),
		routes:        make(map[int]*logbus.Route),
		procSnapCache: newProcSnapshotCache(procSnapMinAge),
	}
	// M6 cluster mode: write the reuseport preload into PM0_HOME and
	// hand its path to the supervisor (cluster launches --require it).
	// Best effort: a read-only home fails cluster STARTS loudly later
	// (WithResolvedExec + launch), not the whole daemon boot.
	if home := store.Home(); home != "" {
		if path, err := cluster.WritePreload(home); err == nil {
			s.sup.SetClusterPreload(path)
		}
	}
	// M5 ops loop: memory pressure / cron / health checks / watch restart
	// apps through the same supervisor.Restart as `pm0 restart`.
	s.opsRecon = ops.New(
		func(id int) error { return s.sup.Restart(strconv.Itoa(id)) },
		func(id int) int64 { return s.treeRSS(id) },
		func(id int) (string, bool) { return s.appStatus(id) },
		func(format string, args ...any) { fmt.Printf("pm0 daemon: "+format+"\n", args...) },
	)
	return s
}

// Run binds the socket and serves until the process dies. It blocks.
func (s *Server) Run() error {
	if err := store.EnsureHome(); err != nil {
		return err
	}

	// Opt-in profiling endpoint (PM0_PPROF_ADDR=127.0.0.1:6060): net/http/pprof
	// on localhost only, started best-effort — a busy port must never stop the
	// daemon. Off by default; zero cost when unset.
	if addr := os.Getenv("PM0_PPROF_ADDR"); addr != "" {
		go func() {
			if err := http.ListenAndServe(addr, nil); err != nil {
				fmt.Printf("pm0 daemon: pprof on %s: %v\n", addr, err)
			}
		}()
	}

	// Verdict on the PREVIOUS owner BEFORE this boot clobbers the record:
	// writePidFile below replaces the file with OUR live identity, and the
	// takeover loop must not read its own record back as "the owner is
	// alive" — that self-clobber is exactly what turned a provable stale
	// takeover back into the full 2s grace (measured 2.1s in the
	// daemon_crash scenario).
	staleOwner := pidFileOwnerDead()
	// Record our identity BEFORE binding: a concurrent boot (two CLI
	// commands racing) must always see a live owner and keep the grace —
	// this is the pidfile-era replacement for the old stat→ping→unlink
	// order's 2s wait (pidfile_linux.go).
	if err := writePidFile(); err != nil {
		fmt.Printf("pm0 daemon: %v (takeover after unclean death will wait the full grace)\n", err)
	}
	defer removePidFileIfOurs()

	// Own the control socket (bind-first; refuses to double-start). The
	// stale-socket takeover is pidfile-accelerated: a provably dead owner
	// is unlinked after a short delay instead of the full 2s grace.
	lis, err := s.takeoverSocket(staleOwner)
	if err != nil {
		return err
	}
	defer lis.Close()
	if err := os.Chmod(store.SocketPath(), 0o600); err != nil {
		return fmt.Errorf("daemon: chmod socket: %w", err)
	}

	gs := grpc.NewServer()
	v1.RegisterDaemonServer(gs, s)

	// Re-exec boot: re-adopt the persisted app set (children survived).
	if os.Getenv(updateFlagEnv) == "1" {
		go func() {
			started, errored := s.adoptOrStartFromDump()
			fmt.Printf("pm0 daemon: update complete: adopted/started %d, errored %d\n", started, errored)
		}()
	}

	// M5 ops enforcers: the registry copy is version-gated (lazy ops
	// tick); runners tick on their own goroutines. Stops with the process.
	go s.opsLoop()

	// Post-flood RSS reclaim (perf queue item 5): return flood-scoured
	// heap pages to the OS once activity quiets down.
	go s.memoryJanitor()

	// SIGTERM/SIGINT: stop every app first (I1), then exit. Children are
	// never orphaned by a polite daemon death.
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		fmt.Printf("pm0 daemon: %v: stopping all apps and exiting\n", sig)
		_ = os.Remove(store.SocketPath())
		removePidFileIfOurs()
		s.stopAll()
		os.Exit(0)
	}()

	return gs.Serve(lis)
}

// takeover timings: a provably-dead owner (pidfile) is unlinked after
// takeoverDelay — matching PM2's measured ~0.33s stale takeover — while
// an unknown owner keeps bootGrace (the historical 2s mid-boot window).
const (
	takeoverDelay = 300 * time.Millisecond
	bootGrace     = 2 * time.Second
)

// pingSocket reports whether a live daemon answers on the socket.
func pingSocket() error {
	conn, err := grpc.NewClient("unix://"+store.SocketPath(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	c := v1.NewDaemonClient(conn)
	_, err = c.Ping(ctx, &v1.PingRequest{})
	return err
}

// takeoverSocket binds the control socket, refusing to double-start.
// Binding is the ownership primitive: net.Listen on a unix path is
// atomic, so a successful bind owns the path and no live socket is ever
// unlinked while it might answer.
//
//   - bind succeeds → we own the socket (fresh home, or stale file
//     nobody defended — the common post-crash case, taken over in
//     ~takeoverDelay when the pidfile proves the previous owner dead);
//   - bind fails → someone holds or races for the path:
//     a live daemon answers ping → ErrAlreadyRunning;
//     pidfile owner provably dead → after takeoverDelay, unlink+retry;
//     unknown owner → after bootGrace (mid-boot daemon window), unlink+retry;
//     if the retry bind still fails, someone else won — keep polling
//     until it answers (ErrAlreadyRunning) or we win the path.
//
// (Replaces socketAliveOrBooting: the old stat→ping→grace→unlink flow
// paid the full 2s grace after every unclean daemon death.)
func (s *Server) takeoverSocket(staleOwner bool) (net.Listener, error) {
	path := store.SocketPath()
	start := time.Now()
	for {
		lis, err := net.Listen("unix", path)
		if err == nil {
			if d := time.Since(start); d > takeoverDelay {
				fmt.Printf("pm0 daemon: control socket acquired in %s\n", d.Round(time.Millisecond))
			}
			return lis, nil
		}
		// Someone holds (or races for) the path. A live daemon answers ping:
		// this home is owned — never touch the socket.
		if pingSocket() == nil {
			return nil, ErrAlreadyRunning
		}
		waited := time.Since(start)
		if waited >= bootGrace || (staleOwner && waited >= takeoverDelay) {
			_ = os.Remove(path)
			if lis, lerr := net.Listen("unix", path); lerr == nil {
				fmt.Printf("pm0 daemon: took over stale control socket in %s\n",
					time.Since(start).Round(time.Millisecond))
				return lis, nil
			}
			// Retry bind lost: the winner is mid-boot — keep polling; its
			// ping will answer and end this loop with ErrAlreadyRunning.
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// resolveSelector expands a Selector into app views, sorted by pm_id,
// deduplicated. Empty selector means all (proto contract).
func (s *Server) resolveSelector(sel *v1.Selector) []supervisor.AppView {
	var out []supervisor.AppView
	seen := make(map[int]bool)
	add := func(views []supervisor.AppView) {
		for _, v := range views {
			if !seen[v.ID] {
				seen[v.ID] = true
				out = append(out, v)
			}
		}
	}

	if sel == nil || (sel.All && len(sel.Names) == 0 && len(sel.PmIds) == 0) {
		add(s.sup.List())
		sortViews(out)
		return out
	}

	for _, id := range sel.PmIds {
		if v, ok := s.sup.Describe(strconv.Itoa(int(id))); ok {
			add([]supervisor.AppView{v})
		}
	}
	for _, name := range sel.Names {
		add(s.resolveNameFamily(name))
	}
	sortViews(out)
	return out
}

// resolveNameFamily matches an app name the pm2 way: exact matches plus the
// fork-instance family <name>-0..<name>-N (§3.2 instances naming), so
// `stop web` covers web-0/web-1/....
func (s *Server) resolveNameFamily(name string) []supervisor.AppView {
	views := s.sup.List()
	var out []supervisor.AppView
	for _, v := range views {
		if v.Config.Name == name || isInstanceOf(v.Config.Name, name) {
			out = append(out, v)
		}
	}
	return out
}

// isInstanceOf reports whether app is named <base>-<digits>.
func isInstanceOf(app, base string) bool {
	if !strings.HasPrefix(app, base+"-") {
		return false
	}
	suffix := app[len(base)+1:]
	if suffix == "" {
		return false
	}
	for _, r := range suffix {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func sortViews(views []supervisor.AppView) {
	sort.Slice(views, func(i, j int) bool { return views[i].ID < views[j].ID })
}

// stopAll stops every registered app (KillDaemon non-force, daemon
// SIGTERM). Errors are swallowed: shutdown must complete even if one kill
// path reports survivors.
func (s *Server) stopAll() {
	for _, v := range s.sup.List() {
		_ = s.sup.Stop(strconv.Itoa(v.ID))
	}
}

// Ping returns daemon metadata (liveness probe + doctor echo).
func (s *Server) Ping(ctx context.Context, req *v1.PingRequest) (*v1.PingResponse, error) {
	d := proc.Detect()
	return &v1.PingResponse{
		Version:       s.version,
		Commit:        s.commit,
		Pid:           int32(os.Getpid()),
		KillMode:      string(s.launch.Mode()),
		KernelRelease: d.KernelRelease,
		CgroupWarning: s.launch.Warning(),
	}, nil
}

// KillDaemon stops all apps (unless force), unlinks the socket and exits.
// The reply is sent first; the shutdown runs detached.
func (s *Server) KillDaemon(ctx context.Context, req *v1.KillDaemonRequest) (*v1.KillDaemonResponse, error) {
	force := req.GetForce()
	go func() {
		time.Sleep(300 * time.Millisecond) // let the gRPC reply flush
		if !force {
			s.stopAll()
		}
		_ = os.Remove(store.SocketPath())
		removePidFileIfOurs()
		os.Exit(0)
	}()
	return &v1.KillDaemonResponse{}, nil
}

// UpdateDaemon saves the app set and re-execs this process in place: the
// children survive (same PID is their parent), the new image re-adopts
// them via updateFlagEnv. The stream reports the stages.
func (s *Server) UpdateDaemon(req *v1.UpdateDaemonRequest, stream v1.Daemon_UpdateDaemonServer) error {
	if err := stream.Send(&v1.UpdateDaemonEvent{Stage: "saving"}); err != nil {
		return err
	}
	if _, err := s.Save(context.Background(), &v1.SaveRequest{}); err != nil {
		return fmt.Errorf("update: save before re-exec: %w", err)
	}
	if err := stream.Send(&v1.UpdateDaemonEvent{Stage: "re-execing"}); err != nil {
		return err
	}
	// Give the stream a moment to drain before the process image is
	// replaced (no ack exists for server streams).
	time.Sleep(200 * time.Millisecond)

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("update: resolve executable: %w", err)
	}
	env := append(os.Environ(), updateFlagEnv+"=1")
	return syscall.Exec(exe, append([]string{exe}, os.Args[1:]...), env)
}
