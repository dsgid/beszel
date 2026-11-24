package systems

// DSG patch: read host stats from SigNoz instead of a beszel-agent.
//
// The hosts already run an OpenTelemetry collector (hostmetrics receiver) that ships to SigNoz, so
// the hub asks SigNoz for the latest samples and hands the result to the same pipeline an agent
// response goes through. Records, rollups and alerts need no changes.
//
// Environment:
//   - SIGNOZ_URL: base URL of SigNoz, for example https://signoz.dsg.id
//   - SIGNOZ_API_KEY: a SigNoz API key with the Viewer role
//   - SIGNOZ_SYSTEMS: comma-separated "System name=host.name" pairs. Only the listed systems read
//     from SigNoz; every other system keeps using its agent. Names match case-insensitively.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/henrygd/beszel"
	"github.com/henrygd/beszel/internal/common"
	"github.com/henrygd/beszel/internal/entities/container"
	"github.com/henrygd/beszel/internal/entities/system"
	"github.com/henrygd/beszel/internal/entities/systemd"
	"github.com/henrygd/beszel/internal/hub/utils"
)

// signozAgentVersion is reported as the agent version of a SigNoz-sourced system. It stays a valid
// version so the UI's version gates still pass; the suffix tells the UI which sections have no data.
var signozAgentVersion = beszel.Version + "-signoz"

const (
	// signozWindow is how far back the latest sample is searched. The hostmetrics interval is 30s
	// and SigNoz alerts on five silent minutes, so a shorter window would flap.
	signozWindow         = 5 * time.Minute
	signozStep           = time.Minute
	signozRequestTimeout = 25 * time.Second
)

type signozSource struct {
	url    string
	key    string
	hosts  map[string]string // lower-case system name -> host.name
	client *http.Client
	now    func() time.Time
}

// newSignozSourceFromEnv returns nil unless SIGNOZ_URL, SIGNOZ_API_KEY and at least one mapping are set.
func newSignozSourceFromEnv() *signozSource {
	url, _ := utils.GetEnv("SIGNOZ_URL")
	key, _ := utils.GetEnv("SIGNOZ_API_KEY")
	systems, _ := utils.GetEnv("SIGNOZ_SYSTEMS")
	hosts := parseSignozSystems(systems)
	if url == "" || key == "" || len(hosts) == 0 {
		return nil
	}
	return &signozSource{
		url:    strings.TrimRight(url, "/"),
		key:    key,
		hosts:  hosts,
		client: &http.Client{Timeout: 20 * time.Second},
		now:    time.Now,
	}
}

func parseSignozSystems(value string) map[string]string {
	hosts := map[string]string{}
	for pair := range strings.SplitSeq(value, ",") {
		name, host, ok := strings.Cut(pair, "=")
		name, host = strings.ToLower(strings.TrimSpace(name)), strings.TrimSpace(host)
		if ok && name != "" && host != "" {
			hosts[name] = host
		}
	}
	return hosts
}

// hostFor returns the SigNoz host.name of a system, if it reads from SigNoz.
func (s *signozSource) hostFor(systemName string) (string, bool) {
	if s == nil {
		return "", false
	}
	host, ok := s.hosts[strings.ToLower(strings.TrimSpace(systemName))]
	return host, ok
}

type signozRow struct {
	labels map[string]string
	value  float64
}

type signozResult map[string][]signozRow

func metricQuery(name, metric, timeAgg, spaceAgg, host string, groupBy ...string) map[string]any {
	group := make([]map[string]string, 0, len(groupBy))
	for _, g := range groupBy {
		group = append(group, map[string]string{"name": g})
	}
	return map[string]any{
		"type": "builder_query",
		"spec": map[string]any{
			"name":   name,
			"signal": "metrics",
			"aggregations": []map[string]string{{
				"metricName":       metric,
				"timeAggregation":  timeAgg,
				"spaceAggregation": spaceAgg,
				"reduceTo":         "last",
			}},
			"filter":  map[string]string{"expression": fmt.Sprintf("host.name = '%s'", strings.ReplaceAll(host, "'", `\'`))},
			"groupBy": group,
			"limit":   1000,
		},
	}
}

func (s *signozSource) query(ctx context.Context, host string) (signozResult, error) {
	// Align the end to a step so the last bucket is complete; a partial bucket skews rates.
	end := s.now().Truncate(signozStep)
	start := end.Add(-signozWindow)
	queries := []map[string]any{
		metricQuery("cpu", "system.cpu.utilization", "avg", "avg", host, "state"),
		metricQuery("cores", "system.cpu.logical.count", "latest", "sum", host),
		metricQuery("mem", "system.memory.usage", "latest", "sum", host, "state"),
		metricQuery("memlimit", "system.memory.limit", "latest", "sum", host),
		metricQuery("swap", "system.paging.usage", "latest", "sum", host, "state"),
		metricQuery("load1", "system.cpu.load_average.1m", "latest", "avg", host),
		metricQuery("load5", "system.cpu.load_average.5m", "latest", "avg", host),
		metricQuery("load15", "system.cpu.load_average.15m", "latest", "avg", host),
		metricQuery("fs", "system.filesystem.usage", "latest", "sum", host, "mountpoint", "state"),
		metricQuery("net", "system.network.io", "rate", "sum", host, "device", "direction"),
		metricQuery("dio", "system.disk.io", "rate", "sum", host, "device", "direction"),
		metricQuery("cpucore", "system.cpu.utilization", "avg", "avg", host, "cpu", "state"),
		metricQuery("uptime", "system.uptime", "latest", "max", host),

		// Containers (docker_stats receiver), one grouped query per metric.
		metricQuery("ccpu", "container.cpu.usage.total", "rate", "sum", host, "container.name"),
		metricQuery("cmem", "container.memory.usage.total", "latest", "sum", host, "container.name"),
		metricQuery("crx", "container.network.io.usage.rx_bytes", "rate", "sum", host, "container.name"),
		metricQuery("ctx", "container.network.io.usage.tx_bytes", "rate", "sum", host, "container.name"),
		metricQuery("cup", "container.uptime", "latest", "max", host, "container.name", "container.id", "container.image.name"),
		metricQuery("chealth", "container.state.health.status", "latest", "max", host, "container.name", "container.state.health.state"),

		// systemd units (systemd receiver).
		metricQuery("ustate", "systemd.unit.state", "latest", "max", host, "systemd.unit.name", "systemd.unit.active_state"),
		metricQuery("ucpu", "systemd.service.cpu.time", "rate", "sum", host, "systemd.unit.name"),
		metricQuery("umem", "systemd.service.memory.usage", "latest", "sum", host, "systemd.unit.name"),
		metricQuery("umempeak", "systemd.service.memory.usage.max", "latest", "max", host, "systemd.unit.name"),
	}
	body, err := json.Marshal(map[string]any{
		"schemaVersion":  "v1",
		"start":          start.UnixMilli(),
		"end":            end.UnixMilli(),
		"requestType":    "scalar",
		"compositeQuery": map[string]any{"queries": queries},
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url+"/api/v5/query_range", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("SIGNOZ-API-KEY", s.key)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("signoz request: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("signoz returned %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	return parseSignozResponse(raw)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func parseSignozResponse(raw []byte) (signozResult, error) {
	var parsed struct {
		Data struct {
			Data struct {
				Results []struct {
					QueryName string `json:"queryName"`
					Columns   []struct {
						Name       string `json:"name"`
						ColumnType string `json:"columnType"`
					} `json:"columns"`
					Data [][]any `json:"data"`
				} `json:"results"`
			} `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("decode signoz response: %w", err)
	}
	out := signozResult{}
	for _, r := range parsed.Data.Data.Results {
		for _, row := range r.Data {
			sr := signozRow{labels: map[string]string{}}
			for i, cell := range row {
				if i >= len(r.Columns) {
					break
				}
				if r.Columns[i].ColumnType == "group" {
					sr.labels[r.Columns[i].Name] = fmt.Sprint(cell)
					continue
				}
				// Aggregation cells arrive as numbers, or as strings for NaN and Inf.
				switch v := cell.(type) {
				case float64:
					sr.value = v
				case string:
					return nil, fmt.Errorf("signoz query %q returned %q instead of a number", r.QueryName, v)
				}
			}
			out[r.QueryName] = append(out[r.QueryName], sr)
		}
	}
	return out, nil
}

// sum returns the total of the rows whose labels all match, and whether any row matched.
func (r signozResult) sum(query string, match map[string]string) (float64, bool) {
	var total float64
	var found bool
nextRow:
	for _, row := range r[query] {
		for k, v := range match {
			if row.labels[k] != v {
				continue nextRow
			}
		}
		total += row.value
		found = true
	}
	return total, found
}

// isPartition reports whether device is another listed device plus a partition suffix (sda1, nvme0n1p2).
func isPartition(device string, devices map[string]bool) bool {
	for other := range devices {
		rest, ok := strings.CutPrefix(device, other)
		if !ok || rest == "" {
			continue
		}
		rest = strings.TrimPrefix(rest, "p")
		if rest != "" && strings.Trim(rest, "0123456789") == "" {
			return true
		}
	}
	return false
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

const gib = 1 << 30

// buildSignozData maps SigNoz host metrics to the data an agent would send.
func buildSignozData(res signozResult, host string, includeDetails bool) (*system.CombinedData, error) {
	if len(res["cpu"]) == 0 || len(res["mem"]) == 0 {
		return nil, fmt.Errorf("no recent host metrics in SigNoz for host.name %q", host)
	}
	var stats system.Stats
	var info system.Info

	// CPU: system.cpu.utilization is the average share of each state across cores. Like the agent,
	// busy time excludes iowait.
	idle, _ := res.sum("cpu", map[string]string{"state": "idle"})
	wait, _ := res.sum("cpu", map[string]string{"state": "wait"})
	stats.Cpu = round2((1 - idle - wait) * 100)
	stats.CpuBreakdown = make([]float64, 8) // user, system, iowait, steal, idle, irq, softirq, nice
	for i, state := range []string{"user", "system", "wait", "steal", "idle", "interrupt", "softirq", "nice"} {
		v, _ := res.sum("cpu", map[string]string{"state": state})
		stats.CpuBreakdown[i] = round2(v * 100)
	}

	// Memory: the hostmetrics states do not add up to the machine total, so the total comes from
	// system.memory.limit. Used is the receiver's used state, which already excludes cache.
	memTotal, ok := res.sum("memlimit", nil)
	if !ok || memTotal <= 0 {
		return nil, fmt.Errorf("host.name %q reports no system.memory.limit; enable it on the host collector", host)
	}
	memUsed, _ := res.sum("mem", map[string]string{"state": "used"})
	cached, _ := res.sum("mem", map[string]string{"state": "cached"})
	buffered, _ := res.sum("mem", map[string]string{"state": "buffered"})
	stats.MemBytes = [4]uint64{uint64(memTotal), uint64(memUsed), uint64(cached + buffered), 0}
	stats.MemPct = round2(memUsed / memTotal * 100)

	swapUsed, _ := res.sum("swap", map[string]string{"state": "used"})
	var swapTotal float64
	for _, row := range res["swap"] {
		swapTotal += row.value
	}
	stats.SwapBytes = [2]uint64{uint64(swapTotal), uint64(swapUsed)}

	// Disk: the root filesystem. Total counts used, free and the reserved blocks. The percentage
	// is used / (used + free), the same as the agent and df: reserved blocks are not usable, so
	// dividing by the full total would read low (about 4 points on a 5 % reserve at 70 % usage).
	var diskUsed, diskTotal float64
	diskUsed, _ = res.sum("fs", map[string]string{"mountpoint": "/", "state": "used"})
	diskFree, _ := res.sum("fs", map[string]string{"mountpoint": "/", "state": "free"})
	for _, row := range res["fs"] {
		if row.labels["mountpoint"] == "/" {
			diskTotal += row.value
		}
	}
	stats.DiskTotal = round2(diskTotal / gib)
	stats.DiskUsed = round2(diskUsed / gib)
	if diskUsed+diskFree > 0 {
		stats.DiskPct = round2(diskUsed / (diskUsed + diskFree) * 100)
	}

	// Network and disk I/O arrive as bytes per second.
	for _, dir := range []string{"transmit", "receive"} {
		v, _ := res.sum("net", map[string]string{"direction": dir})
		if dir == "transmit" {
			stats.Bandwidth[0] = uint64(v)
		} else {
			stats.Bandwidth[1] = uint64(v)
		}
	}
	stats.NetworkInterfaces = map[string][4]uint64{}
	for _, row := range res["net"] {
		ni := stats.NetworkInterfaces[row.labels["device"]]
		if row.labels["direction"] == "transmit" {
			ni[0] = uint64(row.value)
		} else {
			ni[1] = uint64(row.value)
		}
		stats.NetworkInterfaces[row.labels["device"]] = ni
	}
	// Disk I/O: count whole devices only. A partition reports the same traffic as its disk.
	disks := map[string]bool{}
	for _, row := range res["dio"] {
		disks[row.labels["device"]] = true
	}
	for _, row := range res["dio"] {
		if isPartition(row.labels["device"], disks) {
			continue
		}
		if row.labels["direction"] == "read" {
			stats.DiskIO[0] += uint64(row.value)
		} else {
			stats.DiskIO[1] += uint64(row.value)
		}
	}

	for i, q := range []string{"load1", "load5", "load15"} {
		v, _ := res.sum(q, nil)
		stats.LoadAvg[i] = round2(v)
	}

	stats.CpuCoresUsage = coreUsage(res["cpucore"])
	uptime, _ := res.sum("uptime", nil)

	cores, _ := res.sum("cores", nil)
	info = system.Info{
		Threads:        int(cores),
		Cpu:            stats.Cpu,
		MemPct:         stats.MemPct,
		DiskPct:        stats.DiskPct,
		BandwidthBytes: stats.Bandwidth[0] + stats.Bandwidth[1],
		LoadAvg:        stats.LoadAvg,
		Uptime:         uint64(uptime),
		SystemdLogs:    true, // the journald receiver feeds the unit log view
		AgentVersion:   signozAgentVersion,
	}

	data := &system.CombinedData{Stats: stats, Info: info}
	data.Containers = buildContainers(res)
	data.SystemdServices, data.SystemdServicesUpdated = buildSystemdServices(res)
	if includeDetails {
		data.Details = &system.Details{
			Hostname:    host,
			Cores:       int(cores),
			Threads:     int(cores),
			Os:          system.Linux,
			OsName:      "Linux",
			MemoryTotal: uint64(memTotal),
		}
	}
	return data, nil
}

// coreUsage returns the busy share of each core in percent, ordered by core number. Like the host
// total it excludes iowait.
func coreUsage(rows []signozRow) system.Uint8Slice {
	idle := map[int]float64{}
	for _, row := range rows {
		if row.labels["state"] != "idle" && row.labels["state"] != "wait" {
			continue
		}
		n, err := strconv.Atoi(strings.TrimPrefix(row.labels["cpu"], "cpu"))
		if err != nil {
			continue
		}
		idle[n] += row.value
	}
	cores := make([]int, 0, len(idle))
	for n := range idle {
		cores = append(cores, n)
	}
	sort.Ints(cores)
	out := make(system.Uint8Slice, 0, len(cores))
	for _, n := range cores {
		out = append(out, uint8(math.Round(math.Min(100, math.Max(0, (1-idle[n])*100)))))
	}
	return out
}

// dockerUptime formats a container uptime like the status column of docker ps ("Up 7 days").
func dockerUptime(seconds float64) string {
	d := time.Duration(seconds) * time.Second
	plural := func(n int, unit string) string {
		if n == 1 {
			return fmt.Sprintf("Up %d %s", n, unit)
		}
		return fmt.Sprintf("Up %d %ss", n, unit)
	}
	switch {
	case d >= 24*time.Hour:
		return plural(int(d/(24*time.Hour)), "day")
	case d >= time.Hour:
		return plural(int(d/time.Hour), "hour")
	case d >= time.Minute:
		return plural(int(d/time.Minute), "minute")
	default:
		return plural(int(d/time.Second), "second")
	}
}

// buildContainers maps docker_stats metrics to the container list an agent would send. Ports have no
// OTel source and stay empty. Containers are ordered by name so the output is stable.
func buildContainers(res signozResult) []*container.Stats {
	byName := map[string]*container.Stats{}
	get := func(name string) *container.Stats {
		c := byName[name]
		if c == nil {
			c = &container.Stats{Name: name}
			byName[name] = c
		}
		return c
	}
	for _, row := range res["cup"] {
		name := row.labels["container.name"]
		if name == "" {
			continue
		}
		c := get(name)
		c.Id = truncate(row.labels["container.id"], 12)
		c.Image = row.labels["container.image.name"]
		c.Status = dockerUptime(row.value)
	}
	for _, row := range res["ccpu"] {
		if row.labels["container.name"] != "" {
			get(row.labels["container.name"]).Cpu = round2(row.value / 1e9 * 100) // ns of CPU per second to percent of one core, like docker stats
		}
	}
	for _, row := range res["cmem"] {
		if row.labels["container.name"] != "" {
			get(row.labels["container.name"]).Mem = round2(row.value / (1 << 20))
		}
	}
	for _, row := range res["ctx"] {
		if row.labels["container.name"] != "" {
			get(row.labels["container.name"]).Bandwidth[0] = uint64(row.value)
		}
	}
	for _, row := range res["crx"] {
		if row.labels["container.name"] != "" {
			get(row.labels["container.name"]).Bandwidth[1] = uint64(row.value)
		}
	}
	for _, row := range res["chealth"] {
		if row.value < 1 {
			continue
		}
		if h, ok := container.DockerHealthStrings[row.labels["container.state.health.state"]]; ok && row.labels["container.name"] != "" {
			get(row.labels["container.name"]).Health = h
		}
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]*container.Stats, 0, len(names))
	for _, name := range names {
		out = append(out, byName[name])
	}
	return out
}

// buildSystemdServices maps the systemd receiver's metrics to the unit list an agent would send. The
// receiver reports every *.service unit file; units that are plainly inactive are left out, as an
// agent lists only loaded units. The sub state has no OTel source, so it is derived from the state,
// which shows a finished oneshot service as running. CPU peak is not available and repeats the
// current value. The second result marks the snapshot as fresh, so units that vanished are removed.
func buildSystemdServices(res signozResult) ([]*systemd.Service, bool) {
	// Agents name a unit without its ".service" suffix, and existing records use that name.
	unit := func(row signozRow) string { return strings.TrimSuffix(row.labels["systemd.unit.name"], ".service") }
	if len(res["ustate"]) == 0 {
		return nil, false
	}
	byName := map[string]*systemd.Service{}
	for _, row := range res["ustate"] {
		name := unit(row)
		state := row.labels["systemd.unit.active_state"]
		if name == "" || row.value < 1 || state == "inactive" {
			continue
		}
		svc := &systemd.Service{Name: name, State: systemd.ParseServiceStatus(state)}
		switch state {
		case "active", "reloading", "activating", "deactivating":
			svc.Sub = systemd.SubStateRunning
		case "failed":
			svc.Sub = systemd.SubStateFailed
		default:
			svc.Sub = systemd.SubStateUnknown
		}
		byName[name] = svc
	}
	for _, row := range res["ucpu"] {
		if svc := byName[unit(row)]; svc != nil {
			svc.Cpu = round2(row.value / 1e6 * 100) // microseconds of CPU per second to percent of one core
		}
	}
	for _, row := range res["umem"] {
		if svc := byName[unit(row)]; svc != nil {
			svc.Mem = uint64(math.Max(0, row.value))
		}
	}
	for _, row := range res["umempeak"] {
		if svc := byName[unit(row)]; svc != nil {
			svc.MemPeak = uint64(math.Max(0, row.value))
		}
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]*systemd.Service, 0, len(names))
	for _, name := range names {
		svc := byName[name]
		svc.CpuPeak = svc.Cpu
		if svc.MemPeak < svc.Mem {
			svc.MemPeak = svc.Mem
		}
		out = append(out, svc)
	}
	return out, true
}

// logRows decodes the rows of a raw logs response. The shape is not documented for v5, so the parser
// accepts the timestamp and body at the row level or under "data", and reports what it saw otherwise.
func logRows(raw []byte) ([]logLine, error) {
	var parsed struct {
		Data struct {
			Data struct {
				Results []struct {
					Rows []struct {
						Timestamp any            `json:"timestamp"`
						Data      map[string]any `json:"data"`
					} `json:"rows"`
				} `json:"results"`
			} `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("decode signoz logs response: %w", err)
	}
	var out []logLine
	for _, r := range parsed.Data.Data.Results {
		for _, row := range r.Rows {
			body, _ := row.Data["body"].(string)
			ts := row.Timestamp
			if ts == nil {
				ts = row.Data["timestamp"]
			}
			out = append(out, logLine{ts: fmt.Sprint(ts), body: body})
		}
	}
	return out, nil
}

type logLine struct{ ts, body string }

// formatLogs renders lines oldest first, one per entry, like the text an agent returns.
func formatLogs(lines []logLine) string {
	var b strings.Builder
	for i := len(lines) - 1; i >= 0; i-- { // the query returns newest first
		l := lines[i]
		b.WriteString(strings.TrimRight(l.ts+" "+strings.TrimRight(l.body, "\r\n"), " "))
		b.WriteByte('\n')
	}
	return b.String()
}

// escapeFilter makes a value safe inside a single-quoted filter literal.
func escapeFilter(v string) string {
	return strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(v)
}

const signozLogLimit = 200

// logs returns the newest log lines matching a filter expression.
func (s *signozSource) logs(ctx context.Context, filter string) (string, error) {
	end := s.now()
	start := end.Add(-24 * time.Hour)
	body, err := json.Marshal(map[string]any{
		"schemaVersion": "v1",
		"start":         start.UnixMilli(),
		"end":           end.UnixMilli(),
		"requestType":   "raw",
		"compositeQuery": map[string]any{"queries": []map[string]any{{
			"type": "builder_query",
			"spec": map[string]any{
				"name":   "A",
				"signal": "logs",
				"filter": map[string]string{"expression": filter},
				"order":  []map[string]any{{"key": map[string]string{"name": "timestamp"}, "direction": "desc"}},
				"limit":  signozLogLimit,
			},
		}}},
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url+"/api/v5/query_range", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("SIGNOZ-API-KEY", s.key)
	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("signoz request: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("signoz returned %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}
	lines, err := logRows(raw)
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, truncate(string(raw), 300))
	}
	if len(lines) == 0 {
		return "", nil
	}
	return formatLogs(lines), nil
}

// signozHost returns the SigNoz host.name of the system when it reads from SigNoz.
func (sys *System) signozHost() (string, bool) {
	if sys.manager.signoz == nil {
		return "", false
	}
	record, err := sys.getRecord(sys.manager.hub)
	if err != nil {
		return "", false
	}
	return sys.manager.signoz.hostFor(record.GetString("name"))
}

// signozContainerLogs serves container logs from SigNoz. Records hold the 12 character id and the
// logs carry the full id, so the filter matches the prefix.
func (sys *System) signozContainerLogs(containerID string) (string, bool, error) {
	host, ok := sys.signozHost()
	if !ok {
		return "", false, nil
	}
	ctx, cancel := context.WithTimeout(sys.ctx, signozRequestTimeout)
	defer cancel()
	filter := fmt.Sprintf("resource.host.name = '%s' AND resource.container.id LIKE '%s%%'", escapeFilter(host), escapeFilter(containerID))
	out, err := sys.manager.signoz.logs(ctx, filter)
	return out, true, err
}

// signozSystemdLogs serves a unit's journal entries from SigNoz. Records drop the ".service" suffix.
func (sys *System) signozSystemdLogs(serviceName string) (string, bool, error) {
	host, ok := sys.signozHost()
	if !ok {
		return "", false, nil
	}
	ctx, cancel := context.WithTimeout(sys.ctx, signozRequestTimeout)
	defer cancel()
	filter := fmt.Sprintf("resource.host.name = '%s' AND attribute.journald._SYSTEMD_UNIT = '%s.service'", escapeFilter(host), escapeFilter(serviceName))
	out, err := sys.manager.signoz.logs(ctx, filter)
	return out, true, err
}

const signozNoDetails = "Not available for hosts read through SigNoz: it needs a host agent."

// fetch returns the latest host stats for a system.
func (s *signozSource) fetch(ctx context.Context, host string, includeDetails bool) (*system.CombinedData, error) {
	res, err := s.query(ctx, host)
	if err != nil {
		return nil, err
	}
	return buildSignozData(res, host, includeDetails)
}

// fetchDataFromSignoz serves a system that SIGNOZ_SYSTEMS lists. handled is false for every other system.
func (sys *System) fetchDataFromSignoz(options common.DataRequestOptions) (data *system.CombinedData, handled bool, err error) {
	if sys.manager.signoz == nil {
		return nil, false, nil
	}
	record, err := sys.getRecord(sys.manager.hub)
	if err != nil {
		return nil, true, err
	}
	host, ok := sys.manager.signoz.hostFor(record.GetString("name"))
	if !ok {
		return nil, false, nil
	}
	ctx, cancel := context.WithTimeout(sys.ctx, signozRequestTimeout)
	defer cancel()
	data, err = sys.manager.signoz.fetch(ctx, host, options.IncludeDetails)
	return data, true, err
}

// fromSignoz reports whether the last data for this system came from SigNoz instead of an agent.
func (sys *System) fromSignoz() bool {
	return sys.data != nil && sys.data.Info.AgentVersion == signozAgentVersion
}
