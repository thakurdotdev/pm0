//go:build linux

// Process-control RPCs: start (with fork-instance expansion), stop,
// restart (with --update-env), delete, reload (rolling), scale, list,
// describe, save and resurrect (adopt-or-start).
package daemon

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	v1 "github.com/pm0/pm0/api/v1"
	"github.com/pm0/pm0/internal/config"
	"github.com/pm0/pm0/internal/proc"
	"github.com/pm0/pm0/internal/store"
)

// StartProcess registers and starts one or more apps. Instances>1 expands
// to <name>-0..<name>-N-1 taking consecutive pm_ids (§3.2). Spawn failures
// still register the app (parked errored) — the response carries the real
// status, matching pm2's "bad apps stay listed" behavior.
func (s *Server) StartProcess(ctx context.Context, req *v1.StartProcessRequest) (*v1.StartProcessResponse, error) {
	resp := &v1.StartProcessResponse{}
	for _, sp := range req.GetSpecs() {
		cfg := specToConfig(sp)
		if cfg.Script == "" {
			return nil, status.Error(codes.InvalidArgument, "start: script is required")
		}
		// Interpreter resolution at registration: a missing interpreter
		// fails loudly here, not at first spawn (compat rule: drift is loud).
		resolved, err := config.WithResolvedExec(cfg)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("start %s: %v", cfg.Name, err))
		}
		cfg = resolved

		var cfgs []config.App
		if cfg.Instances == 1 {
			cfgs = []config.App{cfg}
		} else if cfg.ExecMode == "cluster" {
			// M6 cluster group: instances KEEP the app name (observed
			// PM2 7.0.4: jlist shows N same-named rows); the index rides
			// cfg.Instance -> NODE_APP_INSTANCE (machine.launch injects
			// the env pair) and the per-instance log paths (fillPaths).
			cfgs = make([]config.App, 0, cfg.Instances)
			for i := 0; i < cfg.Instances; i++ {
				c := cfg
				c.Instance = i
				cfgs = append(cfgs, c)
			}
		} else {
			cfgs = make([]config.App, 0, cfg.Instances)
			for i := 0; i < cfg.Instances; i++ {
				c := cfg
				c.Name = fmt.Sprintf("%s-%d", cfg.Name, i)
				cfgs = append(cfgs, c)
			}
		}

		ids, err := s.sup.StartApps(cfgs, fillPaths)
		if err != nil {
			// The batch is still registered (spawn failures park errored):
			// surface the error but keep the response informative.
			resp.Processes = append(resp.Processes, s.infosFor(ids)...)
			return resp, status.Error(codes.Internal, err.Error())
		}
		s.ensureRoutesFor(ids)
		resp.Processes = append(resp.Processes, s.infosFor(ids)...)
	}
	return resp, nil
}

// infosFor renders ProcessInfo entries for freshly started ids (monit is
// zero on first observation — CPU needs two samples).
func (s *Server) infosFor(ids []int) []*v1.ProcessInfo {
	out := make([]*v1.ProcessInfo, 0, len(ids))
	for _, id := range ids {
		if id < 0 {
			continue // machine construction failed for this slot
		}
		if v, ok := s.sup.Describe(strconv.Itoa(id)); ok {
			out = append(out, viewToProcessInfo(v, &v1.Monit{}, s.redactEnv))
		}
	}
	return out
}

// StopProcess stops the selected apps (idempotent on stopped apps).
func (s *Server) StopProcess(ctx context.Context, req *v1.StopProcessRequest) (*v1.StopProcessResponse, error) {
	views := s.resolveSelector(req.GetSelector())
	resp := &v1.StopProcessResponse{}
	for _, v := range views {
		if err := s.sup.Stop(strconv.Itoa(v.ID)); err != nil {
			return resp, status.Error(codes.Internal, fmt.Sprintf("stop %s: %v", v.Config.Name, err))
		}
		resp.AffectedPmIds = append(resp.AffectedPmIds, int32(v.ID))
	}
	if len(resp.AffectedPmIds) == 0 {
		return nil, status.Error(codes.NotFound, "no matching app")
	}
	return resp, nil
}

// RestartProcess restarts the selected apps; an updated spec carrying env
// realizes `restart --update-env` (§4: the CLI re-reads the current shell).
func (s *Server) RestartProcess(ctx context.Context, req *v1.RestartProcessRequest) (*v1.RestartProcessResponse, error) {
	views := s.resolveSelector(req.GetSelector())
	var newEnv map[string]string
	if sp := req.GetUpdatedSpec(); sp != nil {
		newEnv = sp.GetEnv()
	}
	resp := &v1.RestartProcessResponse{}
	for _, v := range views {
		var err error
		if newEnv != nil {
			err = s.sup.RestartWithEnv(strconv.Itoa(v.ID), newEnv)
		} else {
			err = s.sup.Restart(strconv.Itoa(v.ID))
		}
		if err != nil {
			return resp, status.Error(codes.Internal, fmt.Sprintf("restart %s: %v", v.Config.Name, err))
		}
		resp.AffectedPmIds = append(resp.AffectedPmIds, int32(v.ID))
	}
	if len(resp.AffectedPmIds) == 0 {
		return nil, status.Error(codes.NotFound, "no matching app")
	}
	return resp, nil
}

// DeleteProcess stops and forgets the selected apps, freeing their pm_ids.
func (s *Server) DeleteProcess(ctx context.Context, req *v1.DeleteProcessRequest) (*v1.DeleteProcessResponse, error) {
	views := s.resolveSelector(req.GetSelector())
	resp := &v1.DeleteProcessResponse{}
	for _, v := range views {
		if err := s.deleteAppView(v); err != nil {
			return resp, status.Error(codes.Internal, fmt.Sprintf("delete %s: %v", v.Config.Name, err))
		}
		resp.AffectedPmIds = append(resp.AffectedPmIds, int32(v.ID))
	}
	if len(resp.AffectedPmIds) == 0 {
		return nil, status.Error(codes.NotFound, "no matching app")
	}
	return resp, nil
}

// ReloadProcess rolls the selected apps one pm_id at a time (PM2 reload,
// God/Reload.js softReload): each instance is replaced AT ITS OWN pm_id
// while the old incarnation keeps serving (cluster instances share the
// port through the reuseport pool, so the roll drops no connections), the
// old worker gets the 'shutdown' node-IPC handoff, and a replacement that
// fails to stabilize within listen_timeout is removed and the old
// incarnation restored. Selector names resolve to the whole family (all
// cluster instances / fork siblings), rolled in instance order.
func (s *Server) ReloadProcess(ctx context.Context, req *v1.ReloadProcessRequest) (*v1.ReloadProcessResponse, error) {
	views := s.resolveSelector(req.GetSelector())
	if len(views) == 0 {
		return nil, status.Error(codes.NotFound, "no matching app")
	}
	sort.Slice(views, func(i, j int) bool {
		vi, vj := views[i], views[j]
		if vi.Config.Instance != vj.Config.Instance {
			return vi.Config.Instance < vj.Config.Instance
		}
		return vi.ID < vj.ID
	})
	ids := make([]int, 0, len(views))
	for _, v := range views {
		ids = append(ids, v.ID)
	}
	resp := &v1.ReloadProcessResponse{}
	affected, err := s.sup.ReloadIDs(ids)
	resp.AffectedPmIds = append(resp.AffectedPmIds, toI32(affected)...)
	if err != nil {
		return resp, status.Error(codes.Internal, fmt.Sprintf("reload: %v", err))
	}
	return resp, nil
}

// ScaleProcess adjusts the instance count of an app family by delta
// (fork mode; names continue the <name>-<index> numbering).
func (s *Server) ScaleProcess(ctx context.Context, req *v1.ScaleProcessRequest) (*v1.ScaleProcessResponse, error) {
	name := req.GetName()
	if name == "" {
		return nil, status.Error(codes.InvalidArgument, "scale: app name required")
	}
	instances := s.resolveNameFamily(name)
	if len(instances) == 0 {
		return nil, status.Errorf(codes.NotFound, "no such app: %s", name)
	}

	resp := &v1.ScaleProcessResponse{}
	delta := int(req.GetDelta())
	if delta == 0 {
		for _, v := range instances {
			resp.AffectedPmIds = append(resp.AffectedPmIds, int32(v.ID))
		}
		return resp, nil
	}

	if delta > 0 {
		// Clone the first instance's config; continue index numbering.
		maxIdx := -1
		for _, v := range instances {
			if idx := instanceIndex(v.Config.Name, name); idx > maxIdx {
				maxIdx = idx
			}
		}
		base := instances[0].Config
		if base.ExecMode == "cluster" {
			// M6 cluster family: same name, next NODE_APP_INSTANCE indices.
			for _, v := range instances {
				if v.Config.Instance > maxIdx {
					maxIdx = v.Config.Instance
				}
			}
		}
		cfgs := make([]config.App, 0, delta)
		for i := 1; i <= delta; i++ {
			c := base
			if base.ExecMode == "cluster" {
				c.Instance = maxIdx + i
			} else {
				c.Name = fmt.Sprintf("%s-%d", name, maxIdx+i)
			}
			cfgs = append(cfgs, c)
		}
		ids, err := s.sup.StartApps(cfgs, fillPaths)
		if err != nil {
			resp.AffectedPmIds = append(resp.AffectedPmIds, toI32(ids)...)
			return resp, status.Error(codes.Internal, err.Error())
		}
		s.ensureRoutesFor(ids)
		resp.AffectedPmIds = append(resp.AffectedPmIds, toI32(ids)...)
		return resp, nil
	}

	// delta < 0: delete the highest-index instances.
	n := -delta
	if n >= len(instances) {
		return nil, status.Errorf(codes.InvalidArgument,
			"scale: cannot remove %d of %d instances (all would go; use delete)", n, len(instances))
	}
	for i := 0; i < n; i++ {
		v := instances[len(instances)-1-i]
		if err := s.deleteAppView(v); err != nil {
			return resp, status.Error(codes.Internal, fmt.Sprintf("scale: delete %s: %v", v.Config.Name, err))
		}
		resp.AffectedPmIds = append(resp.AffectedPmIds, int32(v.ID))
	}
	return resp, nil
}

// instanceIndex extracts the -N suffix of app relative to base (-1 when
// app is not an instance of base).
func instanceIndex(app, base string) int {
	if !isInstanceOf(app, base) {
		return -1
	}
	idx, err := strconv.Atoi(app[len(base)+1:])
	if err != nil {
		return -1
	}
	return idx
}

func toI32(ids []int) []int32 {
	out := make([]int32, 0, len(ids))
	for _, id := range ids {
		if id >= 0 {
			out = append(out, int32(id))
		}
	}
	return out
}

// ListProcesses returns every app with fresh monit numbers. One shared
// /proc pass (min-age cached) attributes all trees — O(processes) total
// instead of O(apps × processes) per-app scans.
func (s *Server) ListProcesses(ctx context.Context, req *v1.ListProcessesRequest) (*v1.ListProcessesResponse, error) {
	views := s.sup.List()
	snap := s.procSnapCache.get()
	orphans := orphanMarkerIndex(snap)
	live := make(map[int]bool, len(views))
	resp := &v1.ListProcessesResponse{Processes: make([]*v1.ProcessInfo, 0, len(views))}
	for _, v := range views {
		live[v.ID] = true
		m := s.monitForPids(v.ID, v.Runtime.Pid, viewTreePids(v, snap, orphans), snap)
		resp.Processes = append(resp.Processes, viewToProcessInfo(v, m, s.redactEnv))
	}
	s.cleanupMonitCache(live)
	return resp, nil
}

// DescribeProcess resolves exactly one app (lowest pm_id wins on instance
// families — pm2 describe shows the first match).
func (s *Server) DescribeProcess(ctx context.Context, req *v1.DescribeProcessRequest) (*v1.DescribeProcessResponse, error) {
	views := s.resolveSelector(req.GetSelector())
	if len(views) == 0 {
		return nil, status.Error(codes.NotFound, "no matching app")
	}
	v := views[0]
	snap := s.procSnapCache.get()
	m := s.monitForPids(v.ID, v.Runtime.Pid, viewTreePids(v, snap, orphanMarkerIndex(snap)), snap)
	return &v1.DescribeProcessResponse{Process: viewToProcessInfo(v, m, s.redactEnv)}, nil
}

// Save persists the current app set to dump.json (atomic, I4). Env is
// redacted first when --redact-env is on (§4).
func (s *Server) Save(ctx context.Context, req *v1.SaveRequest) (*v1.SaveResponse, error) {
	views := s.sup.List()
	dump := store.Dump{RedactEnv: s.redactEnv}
	for _, v := range views {
		cfg := v.Config
		if s.redactEnv {
			cfg.Env = redactConfigEnv(cfg.Env)
		}
		dump.Apps = append(dump.Apps, store.Entry{PMID: v.ID, App: cfg})
	}
	if err := store.Save(dump); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &v1.SaveResponse{DumpPath: store.DumpPath(), SavedCount: int32(len(dump.Apps))}, nil
}

// Resurrect adopts surviving trees (daemon crash recovery) or starts the
// dump apps fresh, preserving dump pm_ids (§3.2). One broken entry never
// aborts the batch (proto contract on ResurrectResponse).
func (s *Server) Resurrect(ctx context.Context, req *v1.ResurrectRequest) (*v1.ResurrectResponse, error) {
	started, errored := s.adoptOrStartFromDump()
	return &v1.ResurrectResponse{StartedCount: int32(started), ErroredCount: int32(errored)}, nil
}

// adoptOrStartFromDump loads dump.json and, per entry: adopts the live
// tree when one carries the attribution marker, else starts fresh at the
// persisted pm_id. Returns (started/adopted, errored).
func (s *Server) adoptOrStartFromDump() (started, errored int) {
	dump, ok, err := store.Load()
	if err != nil {
		fmt.Printf("pm0 daemon: resurrect: %v\n", err)
		return 0, 0
	}
	if !ok {
		return 0, 0
	}

	mode := s.launch.Mode()
	cgRoot := s.cgRoot()
	for _, e := range dump.Apps {
		cfg := e.App
		attrib := procAttrib(cfg.Name, e.PMID)

		h, adoptErr := proc.AdoptTree(attrib, mode, cgRoot,
			int(killSignalValue(cfg.KillSignal)), cfg.KillTimeout)
		if adoptErr == nil {
			startEpoch, terr := proc.ProcStartEpochMs(h.PID())
			if terr != nil {
				startEpoch = time.Now().UnixMilli()
			}
			if rerr := s.sup.RegisterAt(cfg, e.PMID, h, time.UnixMilli(startEpoch)); rerr == nil {
				s.ensureRoutesFor([]int{e.PMID})
				started++
				continue
			}
		}
		// No live tree (or adoption impossible): start fresh at the same id.
		if serr := s.sup.StartAtID(cfg, e.PMID); serr == nil {
			s.ensureRoutesFor([]int{e.PMID})
			started++
		} else {
			errored++
			fmt.Printf("pm0 daemon: resurrect %s (pm_id %d): %v\n", cfg.Name, e.PMID, serr)
		}
	}
	return started, errored
}

// procAttrib mirrors machine's attribution naming (sanitized name + pm id).
func procAttrib(name string, pmid int) string {
	return proc.SanitizeName(name) + "-" + strconv.Itoa(pmid)
}

// cgRoot exposes the launcher's cgroup root for adoption lookups.
func (s *Server) cgRoot() string { return s.launch.CgroupRoot() }
