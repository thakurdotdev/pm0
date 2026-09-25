// table.go — human-facing renderers for `pm0 list` and `pm0 describe`.
// The table shape mirrors pm2's default columns; the jlist JSON is the
// load-bearing contract, the table is for humans.
package client

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	v1 "github.com/pm0/pm0/api/v1"
)

type alignment int

const (
	alignLeft alignment = iota
	alignRight
)

// WriteListTable renders pm0's signature process table with clean Unicode borders,
// tree-aware listening port detection, developer-first column hierarchy, and
// an integrated system telemetry overview.
func WriteListTable(w io.Writer, ps []*v1.ProcessInfo) {
	color := isTerminal(w)

	// pm0 column layout: id, app, mode, status, port, pid, uptime, ↺, cpu, mem
	hdr := []string{"id", "app", "mode", "status", "port", "pid", "uptime", "↺", "cpu", "mem"}
	aligns := []alignment{
		alignRight, // id
		alignLeft,  // app
		alignLeft,  // mode
		alignLeft,  // status
		alignLeft,  // port
		alignRight, // pid
		alignLeft,  // uptime
		alignRight, // ↺
		alignRight, // cpu
		alignRight, // mem
	}

	portsByPid := detectAppPorts(ps)

	displayRows := make([][]string, 0, len(ps))

	for _, p := range ps {
		stRaw := statusString(p.GetStatus())
		stDisp := stRaw
		if color {
			stDisp = colorStatus(stRaw)
		}

		modeRaw := "fork"
		if p.GetPm2Env().GetExecMode() == "cluster" || p.GetPm2Env().GetExecMode() == "cluster_mode" {
			modeRaw = "cluster"
		}
		modeDisp := modeRaw
		if color {
			if modeRaw == "cluster" {
				modeDisp = "\033[36mcluster\033[0m"
			} else {
				modeDisp = "\033[90mfork\033[0m"
			}
		}

		leaderPid := int(p.GetPid())
		ports := portsByPid[leaderPid]
		portRaw, portDisp := formatPorts(ports, color)

		restarts := p.GetPm2Env().GetRestartTime()
		restartRaw := fmt.Sprint(restarts)
		restartDisp := restartRaw
		if color {
			if restarts == 0 {
				restartDisp = fmt.Sprintf("\033[90m%d\033[0m", restarts)
			} else if restarts >= 10 {
				restartDisp = fmt.Sprintf("\033[1;31m%d\033[0m", restarts)
			} else {
				restartDisp = fmt.Sprintf("\033[1;33m%d\033[0m", restarts)
			}
		}

		cpuVal := p.GetMonit().GetCpu()
		cpuRaw := fmt.Sprintf("%.1f%%", cpuVal)
		cpuDisp := cpuRaw
		if color {
			if cpuVal == 0.0 {
				cpuDisp = fmt.Sprintf("\033[90m%.1f%%\033[0m", cpuVal)
			} else if cpuVal >= 80.0 {
				cpuDisp = fmt.Sprintf("\033[1;31m%.1f%%\033[0m", cpuVal)
			} else if cpuVal >= 50.0 {
				cpuDisp = fmt.Sprintf("\033[33m%.1f%%\033[0m", cpuVal)
			}
		}

		memVal := p.GetMonit().GetMemory()
		memRaw := memString(memVal)
		memDisp := memRaw
		if color {
			if memVal >= 1024*1024*1024 {
				memDisp = fmt.Sprintf("\033[1;33m%s\033[0m", memRaw)
			}
		}

		rawRow := []string{
			fmt.Sprint(p.GetPmId()),
			p.GetName(),
			modeRaw,
			stRaw,
			portRaw,
			pidString(p),
			uptimeString(p.GetPm2Env().GetPmUptime(), stRaw),
			restartRaw,
			cpuRaw,
			memRaw,
		}

		dispRow := []string{
			rawRow[0],
			rawRow[1],
			modeDisp,
			stDisp,
			portDisp,
			rawRow[5],
			rawRow[6],
			restartDisp,
			cpuDisp,
			memDisp,
		}

		displayRows = append(displayRows, dispRow)
	}

	if len(ps) == 0 {
		printEmptyTable(w, color)
		return
	}

	widths := make([]int, len(hdr))
	for i, h := range hdr {
		widths[i] = visibleLen(h)
	}
	for _, r := range displayRows {
		for i, c := range r {
			if l := visibleLen(c); l > widths[i] {
				widths[i] = l
			}
		}
	}

	totalWidth := 0
	for _, wCol := range widths {
		totalWidth += wCol + 3
	}
	tableWidth := totalWidth - 1

	// 1. Top border: ┌─────┬─────┐
	printBorder(w, "┌", "┬", "┐", widths, color)

	// 2. Header row: │ id  │ app │
	printTableRow(w, hdr, widths, aligns, true, color)

	// 3. Middle border: ├─────┼─────┤
	printBorder(w, "├", "┼", "┤", widths, color)

	// 4. Data rows
	for _, r := range displayRows {
		printTableRow(w, r, widths, aligns, false, color)
	}

	// 5. Table-to-footer divider: ├─────┴─────┤
	printBorder(w, "├", "┴", "┤", widths, color)

	// 6. Integrated footer panel matching exact table width + closing border
	printUsageSummary(w, ps, tableWidth, color)
}

func formatPorts(ports []int, color bool) (raw string, disp string) {
	if len(ports) == 0 {
		if color {
			return "-", "\033[90m-\033[0m"
		}
		return "-", "-"
	}
	switch len(ports) {
	case 1:
		raw = fmt.Sprintf(":%d", ports[0])
	case 2:
		raw = fmt.Sprintf(":%d, :%d", ports[0], ports[1])
	default:
		raw = fmt.Sprintf(":%d (+%d)", ports[0], len(ports)-1)
	}
	if color {
		disp = fmt.Sprintf("\033[36m%s\033[0m", raw)
	} else {
		disp = raw
	}
	return raw, disp
}

func printEmptyTable(w io.Writer, color bool) {
	width := 80
	bColor := ""
	reset := ""
	if color {
		bColor = "\033[90m"
		reset = "\033[0m"
	}
	msg := "No processes managed by pm0"
	padLeft := (width - len(msg)) / 2
	padRight := width - len(msg) - padLeft

	fmt.Fprintf(w, "%s┌%s┐%s\n", bColor, strings.Repeat("─", width), reset)
	fmt.Fprintf(w, "%s│%s%s%s%s%s│%s\n", bColor, reset, strings.Repeat(" ", padLeft), msg, strings.Repeat(" ", padRight), bColor, reset)
	fmt.Fprintf(w, "%s├%s┤%s\n", bColor, strings.Repeat("─", width), reset)
	printUsageSummary(w, nil, width, color)
}

func printBorder(w io.Writer, left, mid, right string, widths []int, color bool) {
	var b strings.Builder
	if color {
		b.WriteString("\033[90m")
	}
	b.WriteString(left)
	for i, wCol := range widths {
		if i > 0 {
			b.WriteString(mid)
		}
		b.WriteString(strings.Repeat("─", wCol+2))
	}
	b.WriteString(right)
	if color {
		b.WriteString("\033[0m")
	}
	fmt.Fprintln(w, b.String())
}

func printTableRow(w io.Writer, cells []string, widths []int, aligns []alignment, isHeader, color bool) {
	var b strings.Builder
	bDivider := "│"
	if color {
		bDivider = "\033[90m│\033[0m"
	}

	b.WriteString(bDivider)
	for i, c := range cells {
		pad := widths[i] - visibleLen(c)
		if pad < 0 {
			pad = 0
		}

		b.WriteString(" ")
		if isHeader && color {
			b.WriteString("\033[1;37m")
		}

		if aligns[i] == alignRight {
			b.WriteString(strings.Repeat(" ", pad))
			b.WriteString(c)
		} else {
			b.WriteString(c)
			b.WriteString(strings.Repeat(" ", pad))
		}

		if isHeader && color {
			b.WriteString("\033[0m")
		}
		b.WriteString(" ")
		b.WriteString(bDivider)
	}
	fmt.Fprintln(w, b.String())
}

func printUsageSummary(w io.Writer, ps []*v1.ProcessInfo, tableWidth int, color bool) {
	var totalMemory int64
	var totalCPU float64
	onlineCount := 0
	erroredCount := 0
	stoppedCount := 0

	for _, p := range ps {
		totalMemory += p.GetMonit().GetMemory()
		totalCPU += p.GetMonit().GetCpu()
		switch statusString(p.GetStatus()) {
		case "online":
			onlineCount++
		case "errored":
			erroredCount++
		case "stopped":
			stoppedCount++
		}
	}

	sys := readSystemStats()

	// 1. Process summary
	var procDisp string
	if len(ps) == 0 {
		if color {
			procDisp = "\033[90mprocesses:\033[0m \033[90m0 processes\033[0m"
		} else {
			procDisp = "processes: 0 processes"
		}
	} else {
		var parts []string
		var dispParts []string
		parts = append(parts, fmt.Sprintf("%d online", onlineCount))
		if color {
			dispParts = append(dispParts, fmt.Sprintf("\033[1;32m%d online\033[0m", onlineCount))
		}
		if erroredCount > 0 {
			parts = append(parts, fmt.Sprintf("%d errored", erroredCount))
			if color {
				dispParts = append(dispParts, fmt.Sprintf("\033[1;31m%d errored\033[0m", erroredCount))
			}
		}
		if stoppedCount > 0 {
			parts = append(parts, fmt.Sprintf("%d stopped", stoppedCount))
			if color {
				dispParts = append(dispParts, fmt.Sprintf("\033[33m%d stopped\033[0m", stoppedCount))
			}
		}

		if color {
			procDisp = "\033[90mprocesses:\033[0m " + strings.Join(dispParts, "\033[90m, \033[0m")
		} else {
			procDisp = "processes: " + strings.Join(parts, ", ")
		}
	}

	// 2. App usage (CPU & Mem)
	cpuRaw := fmt.Sprintf("%.1f%% cpu", totalCPU)
	cpuDisp := cpuRaw
	if color {
		if totalCPU == 0.0 {
			cpuDisp = fmt.Sprintf("\033[90m%.1f%% cpu\033[0m", totalCPU)
		} else if totalCPU >= 80.0 {
			cpuDisp = fmt.Sprintf("\033[1;31m%.1f%% cpu\033[0m", totalCPU)
		} else if totalCPU >= 50.0 {
			cpuDisp = fmt.Sprintf("\033[33m%.1f%% cpu\033[0m", totalCPU)
		}
	}

	memRaw := memString(totalMemory) + " mem"
	memDisp := memRaw
	if color && totalMemory >= 1024*1024*1024 {
		memDisp = fmt.Sprintf("\033[1;33m%s\033[0m", memRaw)
	}

	var appDisp string
	if color {
		appDisp = fmt.Sprintf("\033[90mapp usage:\033[0m %s\033[90m, \033[0m%s", cpuDisp, memDisp)
	} else {
		appDisp = fmt.Sprintf("app usage: %s, %s", cpuRaw, memRaw)
	}

	// 3. Host CPU & Load
	var hostCPUDisp string
	if sys.HasLoad {
		if color {
			hostCPUDisp = fmt.Sprintf("\033[90mhost cpu:\033[0m %d cores \033[90m(load: %.2f, %.2f, %.2f)\033[0m", sys.CPUCores, sys.Load1, sys.Load5, sys.Load15)
		} else {
			hostCPUDisp = fmt.Sprintf("host cpu: %d cores (load: %.2f, %.2f, %.2f)", sys.CPUCores, sys.Load1, sys.Load5, sys.Load15)
		}
	} else {
		if color {
			hostCPUDisp = fmt.Sprintf("\033[90mhost cpu:\033[0m %d cores", sys.CPUCores)
		} else {
			hostCPUDisp = fmt.Sprintf("host cpu: %d cores", sys.CPUCores)
		}
	}

	// 4. Host RAM with mini gauge
	var hostMemDisp string
	if sys.HasMem {
		gaugeRaw, gaugeDisp := renderMiniGauge(sys.MemUsedPct, color)
		var memDetail string
		if tableWidth >= 85 {
			memDetail = fmt.Sprintf("%.1f%% (%s / %s)", sys.MemUsedPct, formatMemBytes(sys.UsedMem), formatMemBytes(sys.TotalMem))
		} else {
			memDetail = fmt.Sprintf("%.1f%% (%s/%s)", sys.MemUsedPct, formatMemBytes(sys.UsedMem), formatMemBytes(sys.TotalMem))
		}

		if color {
			hostMemDisp = fmt.Sprintf("\033[90mhost mem:\033[0m %s %s", gaugeDisp, memDetail)
		} else {
			hostMemDisp = fmt.Sprintf("host mem: %s %s", gaugeRaw, memDetail)
		}
	} else {
		if color {
			hostMemDisp = "\033[90mhost mem: -\033[0m"
		} else {
			hostMemDisp = "host mem: -"
		}
	}

	// Render footer lines inside the unified box
	printFooterLine(w, procDisp, hostCPUDisp, tableWidth, color)
	printFooterLine(w, appDisp, hostMemDisp, tableWidth, color)

	// Bottom border: └─────────────┘
	bColor := ""
	reset := ""
	if color {
		bColor = "\033[90m"
		reset = "\033[0m"
	}
	fmt.Fprintf(w, "%s└%s┘%s\n", bColor, strings.Repeat("─", tableWidth), reset)
}

func printFooterLine(w io.Writer, leftDisp, rightDisp string, tableWidth int, color bool) {
	bDiv := "│"
	if color {
		bDiv = "\033[90m│\033[0m"
	}

	leftLen := visibleLen(leftDisp)
	rightLen := visibleLen(rightDisp)

	// Side-by-side with 2 spaces margin
	needed := leftLen + rightLen + 4
	if needed <= tableWidth {
		pad := tableWidth - 4 - leftLen - rightLen
		fmt.Fprintf(w, "%s  %s%s%s  %s\n", bDiv, leftDisp, strings.Repeat(" ", pad), rightDisp, bDiv)
		return
	}

	// Side-by-side with 1 space margin
	if leftLen+rightLen+2 <= tableWidth {
		pad := tableWidth - 2 - leftLen - rightLen
		fmt.Fprintf(w, "%s %s%s%s %s\n", bDiv, leftDisp, strings.Repeat(" ", pad), rightDisp, bDiv)
		return
	}

	// If narrow terminal, stack vertically within the box with 2 spaces margin
	padLeft := tableWidth - 4 - leftLen
	if padLeft < 0 {
		padLeft = 0
	}
	fmt.Fprintf(w, "%s  %s%s  %s\n", bDiv, leftDisp, strings.Repeat(" ", padLeft), bDiv)

	padRight := tableWidth - 4 - rightLen
	if padRight < 0 {
		padRight = 0
	}
	fmt.Fprintf(w, "%s  %s%s  %s\n", bDiv, rightDisp, strings.Repeat(" ", padRight), bDiv)
}

func renderMiniGauge(pct float64, color bool) (string, string) {
	const totalBlocks = 10
	filled := int((pct + 5.0) / 10.0)
	if filled < 0 {
		filled = 0
	}
	if filled > totalBlocks {
		filled = totalBlocks
	}
	empty := totalBlocks - filled

	raw := fmt.Sprintf("[%s%s]", strings.Repeat("■", filled), strings.Repeat("□", empty))
	if !color {
		return raw, raw
	}

	gaugeColor := "\033[32m" // green
	if pct >= 90.0 {
		gaugeColor = "\033[1;31m" // bright red
	} else if pct >= 75.0 {
		gaugeColor = "\033[33m" // yellow
	}

	disp := fmt.Sprintf("\033[90m[\033[0m%s%s\033[90m%s]\033[0m",
		gaugeColor,
		strings.Repeat("■", filled),
		strings.Repeat("□", empty),
	)
	return raw, disp
}

func colorStatus(status string) string {
	switch status {
	case "online":
		return "\033[1;32m● online\033[0m"
	case "errored":
		return "\033[1;31m✖ errored\033[0m"
	case "stopped":
		return "\033[33m○ stopped\033[0m"
	case "launching":
		return "\033[1;36m◌ launching\033[0m"
	case "stopping":
		return "\033[33m◌ stopping\033[0m"
	case "waiting":
		return "\033[33m◌ waiting\033[0m"
	default:
		return status
	}
}

// detectAppPorts identifies listening TCP/TCP6 ports for each managed process.
// It checks the leader process directly, and if wrapped (e.g. pnpm, sh, next),
// scans the process group and descendant hierarchy.
func detectAppPorts(ps []*v1.ProcessInfo) map[int][]int {
	if runtime.GOOS != "linux" || len(ps) == 0 {
		return nil
	}

	inodeToPort := make(map[uint64]int)
	for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		lines := strings.Split(string(data), "\n")
		for _, line := range lines[1:] {
			fields := strings.Fields(line)
			if len(fields) < 10 {
				continue
			}
			if fields[3] != "0A" { // 0A = TCP_LISTEN
				continue
			}
			parts := strings.Split(fields[1], ":")
			if len(parts) == 2 {
				port64, _ := strconv.ParseUint(parts[1], 16, 32)
				inode, _ := strconv.ParseUint(fields[9], 10, 64)
				if inode > 0 && port64 > 0 {
					inodeToPort[inode] = int(port64)
				}
			}
		}
	}
	if len(inodeToPort) == 0 {
		return nil
	}

	res := make(map[int][]int)
	var needTreeScan []int

	// Fast path: inspect leader PID directly
	for _, p := range ps {
		pid := int(p.GetPid())
		if pid <= 0 {
			continue
		}
		ports := findSocketsForPid(pid, inodeToPort)
		if len(ports) > 0 {
			res[pid] = ports
		} else {
			needTreeScan = append(needTreeScan, pid)
		}
	}

	// If all apps found their ports on the leader PID, we are done!
	if len(needTreeScan) == 0 {
		return res
	}

	// Tree path: scan /proc once to map pgrp and ppid for remaining apps
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return res
	}

	pgrpToPids := make(map[int][]int)
	ppidToPids := make(map[int][]int)
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		statData, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			continue
		}
		statStr := string(statData)
		rparen := strings.LastIndexByte(statStr, ')')
		if rparen < 0 {
			continue
		}
		rest := strings.Fields(statStr[rparen+1:])
		if len(rest) >= 3 {
			ppid, _ := strconv.Atoi(rest[1])
			pgrp, _ := strconv.Atoi(rest[2])
			if pgrp > 0 {
				pgrpToPids[pgrp] = append(pgrpToPids[pgrp], pid)
			}
			if ppid > 0 {
				ppidToPids[ppid] = append(ppidToPids[ppid], pid)
			}
		}
	}

	for _, leaderPid := range needTreeScan {
		pidSet := make(map[int]bool)
		pidSet[leaderPid] = true
		for _, p := range pgrpToPids[leaderPid] {
			pidSet[p] = true
		}
		var queue []int
		for p := range pidSet {
			queue = append(queue, p)
		}
		for len(queue) > 0 {
			curr := queue[0]
			queue = queue[1:]
			for _, child := range ppidToPids[curr] {
				if !pidSet[child] {
					pidSet[child] = true
					queue = append(queue, child)
				}
			}
		}

		var allPorts []int
		seenPort := make(map[int]bool)
		for pid := range pidSet {
			ports := findSocketsForPid(pid, inodeToPort)
			for _, port := range ports {
				if !seenPort[port] {
					seenPort[port] = true
					allPorts = append(allPorts, port)
				}
			}
		}
		if len(allPorts) > 0 {
			sort.Ints(allPorts)
			res[leaderPid] = allPorts
		}
	}

	return res
}

func findSocketsForPid(pid int, inodeToPort map[uint64]int) []int {
	fds, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
	if err != nil {
		return nil
	}
	var ports []int
	seen := make(map[int]bool)
	for _, fd := range fds {
		target, err := os.Readlink(filepath.Join(fmt.Sprintf("/proc/%d/fd", pid), fd.Name()))
		if err != nil {
			continue
		}
		if strings.HasPrefix(target, "socket:[") && strings.HasSuffix(target, "]") {
			inodeStr := target[8 : len(target)-1]
			inode, err := strconv.ParseUint(inodeStr, 10, 64)
			if err != nil {
				continue
			}
			if port, ok := inodeToPort[inode]; ok {
				if !seen[port] {
					seen[port] = true
					ports = append(ports, port)
				}
			}
		}
	}
	sort.Ints(ports)
	return ports
}

func visibleLen(s string) int {
	inEscape := false
	n := 0
	for _, r := range s {
		if r == '\x1b' {
			inEscape = true
			continue
		}
		if inEscape {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEscape = false
			}
			continue
		}
		n++
	}
	return n
}

func isTerminal(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && (st.Mode()&os.ModeCharDevice) != 0
}

type SystemStats struct {
	TotalMem   uint64
	UsedMem    uint64
	MemUsedPct float64
	CPUCores   int
	Load1      float64
	Load5      float64
	Load15     float64
	HasMem     bool
	HasLoad    bool
}

func readSystemStats() SystemStats {
	stats := SystemStats{
		CPUCores: runtime.NumCPU(),
	}

	if data, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) >= 3 {
			stats.Load1, _ = strconv.ParseFloat(fields[0], 64)
			stats.Load5, _ = strconv.ParseFloat(fields[1], 64)
			stats.Load15, _ = strconv.ParseFloat(fields[2], 64)
			stats.HasLoad = true
		}
	}

	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		lines := strings.Split(string(data), "\n")
		var totalKB, availKB uint64
		for _, line := range lines {
			if strings.HasPrefix(line, "MemTotal:") {
				totalKB = parseMemKB(line)
			} else if strings.HasPrefix(line, "MemAvailable:") {
				availKB = parseMemKB(line)
			}
		}
		if totalKB > 0 {
			stats.TotalMem = totalKB * 1024
			if totalKB >= availKB {
				stats.UsedMem = (totalKB - availKB) * 1024
				stats.MemUsedPct = float64(totalKB-availKB) / float64(totalKB) * 100
			}
			stats.HasMem = true
		}
	}

	return stats
}

func parseMemKB(line string) uint64 {
	fields := strings.Fields(line)
	if len(fields) >= 2 {
		val, _ := strconv.ParseUint(fields[1], 10, 64)
		return val
	}
	return 0
}

func formatMemBytes(b uint64) string {
	const gb = 1024 * 1024 * 1024
	const mb = 1024 * 1024
	if b >= gb {
		return fmt.Sprintf("%.1f GB", float64(b)/float64(gb))
	}
	if b >= mb {
		return fmt.Sprintf("%.1f MB", float64(b)/float64(mb))
	}
	return fmt.Sprintf("%d B", b)
}

// pidString: 0 when not running (§2.1).
func pidString(p *v1.ProcessInfo) string {
	if p.GetPid() == 0 {
		return "0"
	}
	return fmt.Sprint(p.GetPid())
}

// uptimeString renders time since pm_uptime ("3s", "2m", "5h", "4d");
// "0" when never started or not running.
func uptimeString(pmUptimeMs int64, status string) string {
	if pmUptimeMs <= 0 {
		return "0"
	}
	switch status {
	case "stopped", "errored", "waiting":
		return "0"
	}
	d := time.Since(time.UnixMilli(pmUptimeMs))
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// memString renders bytes pm2-style ("46.2mb").
func memString(b int64) string {
	const mb = 1024 * 1024
	switch {
	case b >= mb:
		return fmt.Sprintf("%.1fmb", float64(b)/mb)
	case b >= 1024:
		return fmt.Sprintf("%.1fkb", float64(b)/1024)
	default:
		return fmt.Sprintf("%db", b)
	}
}

// WriteDescribe renders `pm0 describe` (key/value block, pm2 order).
func WriteDescribe(w io.Writer, p *v1.ProcessInfo) {
	e := p.GetPm2Env()
	kv := []struct{ k, v string }{
		{"status", statusString(p.GetStatus())},
		{"name", p.GetName()},
		{"pm_id", fmt.Sprint(p.GetPmId())},
		{"pid", fmt.Sprint(p.GetPid())},
		{"script", e.GetPmExecPath()},
		{"args", strings.Join(e.GetArgs(), " ")},
		{"interpreter", e.GetInterpreter()},
		{"exec mode", e.GetExecMode()},
		{"instances", fmt.Sprint(e.GetInstances())},
		{"namespace", e.GetNamespace()},
		{"cwd", e.GetPmCwd()},
		{"restarts", fmt.Sprint(e.GetRestartTime())},
		{"unstable restarts", fmt.Sprint(e.GetUnstableRestarts())},
		{"uptime", uptimeString(e.GetPmUptime(), statusString(p.GetStatus()))},
		{"created at", time.UnixMilli(e.GetCreatedAt()).Format(time.RFC3339)},
		{"last exit code", describeExit(e)},
		{"exit reason", exitReasonString(p.GetExitReason())},
		{"kill signal", e.GetKillSignal()},
		{"kill timeout", fmt.Sprintf("%dms", e.GetKillTimeoutMs())},
		{"autorestart", fmt.Sprint(e.GetAutorestart())},
		{"max restarts", fmt.Sprint(e.GetMaxRestarts())},
		{"min uptime", fmt.Sprintf("%dms", e.GetMinUptimeMs())},
		{"restart delay", fmt.Sprintf("%dms", e.GetRestartDelayMs())},
		{"exp backoff", fmt.Sprint(e.GetExpBackoffRestartTime())},
		{"watch", fmt.Sprint(e.GetWatch())},
		{"treekill", fmt.Sprint(e.GetTreekill())},
		{"out log", e.GetPmOutLogPath()},
		{"err log", e.GetPmErrLogPath()},
		{"pid file", e.GetPmPidPath()},
		{"memory", memString(p.GetMonit().GetMemory())},
		{"cpu", fmt.Sprintf("%.1f%%", p.GetMonit().GetCpu())},
	}
	width := 0
	for _, x := range kv {
		if len(x.k) > width {
			width = len(x.k)
		}
	}
	for _, x := range kv {
		fmt.Fprintf(w, "%-*s : %s\n", width, x.k, x.v)
	}
}

func describeExit(e *v1.ProcessEnv) string {
	if !e.GetExitCodePresent() {
		return "null"
	}
	return fmt.Sprint(e.GetExitCode())
}

// exitReasonString renders the M5 exit reason (divergence 6): "oom" for a
// kernel OOM kill, "-" when unknown/not applicable (additive pm0 field).
func exitReasonString(reason string) string {
	if reason == "" {
		return "-"
	}
	return reason
}
