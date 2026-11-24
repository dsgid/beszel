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
	"strings"
	"time"

	"github.com/henrygd/beszel/internal/common"
	"github.com/henrygd/beszel/internal/entities/system"
	"github.com/henrygd/beszel/internal/hub/utils"
)

const (
	signozAgentVersion = "signoz"
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
			"limit":   100,
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

	cores, _ := res.sum("cores", nil)
	info = system.Info{
		Threads:        int(cores),
		Cpu:            stats.Cpu,
		MemPct:         stats.MemPct,
		DiskPct:        stats.DiskPct,
		BandwidthBytes: stats.Bandwidth[0] + stats.Bandwidth[1],
		LoadAvg:        stats.LoadAvg,
		AgentVersion:   signozAgentVersion,
	}

	data := &system.CombinedData{Stats: stats, Info: info}
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
