package systems

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/henrygd/beszel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixture is shaped like a real SigNoz scalar response for dsg-fringe.
func signozFixture(t *testing.T, results ...map[string]any) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"status": "success",
		"data":   map[string]any{"type": "scalar", "data": map[string]any{"results": results}},
	})
	require.NoError(t, err)
	return body
}

func signozResultSet(name string, groups []string, rows [][]any) map[string]any {
	cols := []map[string]string{}
	for _, g := range groups {
		cols = append(cols, map[string]string{"name": g, "columnType": "group"})
	}
	cols = append(cols, map[string]string{"name": "__result_0", "columnType": "aggregation"})
	return map[string]any{"queryName": name, "columns": cols, "data": rows}
}

func fringeFixture(t *testing.T) []byte {
	return signozFixture(t,
		signozResultSet("cpu", []string{"state"}, [][]any{{"idle", 0.63}, {"user", 0.192}, {"system", 0.149}, {"wait", 0.022}, {"softirq", 0.00677}, {"steal", 0.0}, {"interrupt", 0.0}, {"nice", 0.0}}),
		signozResultSet("cores", nil, [][]any{{8.0}}),
		signozResultSet("mem", []string{"state"}, [][]any{{"used", 10007760896.0}, {"cached", 5174513664.0}, {"free", 1955749888.0}, {"slab_reclaimable", 1560866816.0}, {"slab_unreclaimable", 744013824.0}, {"buffered", 116617216.0}}),
		signozResultSet("memlimit", nil, [][]any{{16704655360.0}}),
		signozResultSet("swap", []string{"state"}, [][]any{{"used", 1073741824.0}, {"free", 3221225472.0}}),
		signozResultSet("load1", nil, [][]any{{1.5}}),
		signozResultSet("load5", nil, [][]any{{1.25}}),
		signozResultSet("load15", nil, [][]any{{1.0}}),
		signozResultSet("fs", []string{"mountpoint", "state"}, [][]any{{"/", "used", 72214413312.0}, {"/", "free", 27958063104.0}, {"/", "reserved", 5277655813.0}, {"/boot", "used", 264437760.0}}),
		signozResultSet("net", []string{"device", "direction"}, [][]any{{"ens3", "receive", 212605.0}, {"ens3", "transmit", 33798.0}, {"wt0", "receive", 9198.0}, {"wt0", "transmit", 8966.0}}),
		signozResultSet("dio", []string{"device", "direction"}, [][]any{{"sda", "read", 1000.0}, {"sda1", "read", 1000.0}, {"sda", "write", 2000.0}, {"sda1", "write", 2000.0}, {"nvme0n1", "read", 10.0}, {"nvme0n1p1", "read", 10.0}}),
	)
}

func TestBuildSignozData(t *testing.T) {
	res, err := parseSignozResponse(fringeFixture(t))
	require.NoError(t, err)
	data, err := buildSignozData(res, "dsg-fringe", true)
	require.NoError(t, err)

	assert.InDelta(t, 34.8, data.Stats.Cpu, 0.001)             // 100 - idle 63 - iowait 2.2
	assert.InDelta(t, 19.2, data.Stats.CpuBreakdown[0], 0.001) // user
	assert.InDelta(t, 63.0, data.Stats.CpuBreakdown[4], 0.001) // idle
	assert.Equal(t, uint64(10007760896), data.Stats.MemBytes[1])
	assert.Equal(t, uint64(5174513664+116617216), data.Stats.MemBytes[2])
	assert.Equal(t, uint64(16704655360), data.Stats.MemBytes[0])
	assert.InDelta(t, 10007760896/16704655360.0*100, data.Stats.MemPct, 0.01)
	assert.Equal(t, [2]uint64{4 << 30, 1 << 30}, data.Stats.SwapBytes)

	// Only the root filesystem counts, and reserved blocks are part of the total.
	assert.InDelta(t, (72214413312.0+27958063104+5277655813)/gib, data.Stats.DiskTotal, 0.01)
	assert.InDelta(t, 72214413312.0/gib, data.Stats.DiskUsed, 0.01)
	// used / (used + free), as df and the agent report (Central B read 72.09 % from its agent).
	assert.InDelta(t, 72.09, data.Stats.DiskPct, 0.01)

	assert.Equal(t, [2]uint64{33798 + 8966, 212605 + 9198}, data.Stats.Bandwidth)
	assert.Equal(t, uint64(33798+8966+212605+9198), data.Info.BandwidthBytes)
	assert.Equal(t, [2]uint64{33798, 212605}, [2]uint64{data.Stats.NetworkInterfaces["ens3"][0], data.Stats.NetworkInterfaces["ens3"][1]})
	assert.Equal(t, [2]uint64{1010, 2000}, data.Stats.DiskIO, "partitions are not counted twice")
	assert.Equal(t, [3]float64{1.5, 1.25, 1.0}, data.Stats.LoadAvg)

	assert.Equal(t, 8, data.Info.Threads)
	assert.Equal(t, signozAgentVersion, data.Info.AgentVersion)
	require.NotNil(t, data.Details)
	assert.Equal(t, "dsg-fringe", data.Details.Hostname)
	assert.Equal(t, 8, data.Details.Cores)
}

func TestBuildSignozDataOmitsDetailsAfterFirstFetch(t *testing.T) {
	res, err := parseSignozResponse(fringeFixture(t))
	require.NoError(t, err)
	data, err := buildSignozData(res, "dsg-fringe", false)
	require.NoError(t, err)
	assert.Nil(t, data.Details)
}

func TestBuildSignozDataRequiresMemoryLimit(t *testing.T) {
	res, err := parseSignozResponse(signozFixture(t,
		signozResultSet("cpu", []string{"state"}, [][]any{{"idle", 0.5}}),
		signozResultSet("mem", []string{"state"}, [][]any{{"used", 1.0}}),
	))
	require.NoError(t, err)
	_, err = buildSignozData(res, "dsg-fringe", false)
	assert.ErrorContains(t, err, "system.memory.limit")
}

func TestIsPartition(t *testing.T) {
	devices := map[string]bool{"sda": true, "sda1": true, "nvme0n1": true, "nvme0n1p2": true, "vda": true}
	for device, want := range map[string]bool{"sda": false, "sda1": true, "nvme0n1": false, "nvme0n1p2": true, "vda": false} {
		assert.Equal(t, want, isPartition(device, devices), device)
	}
}

func TestBuildSignozDataNoRecentMetrics(t *testing.T) {
	res, err := parseSignozResponse(signozFixture(t, signozResultSet("cpu", []string{"state"}, nil)))
	require.NoError(t, err)
	_, err = buildSignozData(res, "dsg-gone", false)
	assert.ErrorContains(t, err, "no recent host metrics")
}

func TestSignozSourceQuery(t *testing.T) {
	var gotKey, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey, gotPath = r.Header.Get("SIGNOZ-API-KEY"), r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write(fringeFixture(t))
	}))
	defer srv.Close()

	now := time.Date(2026, 11, 24, 3, 36, 40, 0, time.UTC)
	src := &signozSource{url: srv.URL, key: "k", hosts: map[string]string{"fringe": "dsg-fringe"}, client: srv.Client(), now: func() time.Time { return now }}
	data, err := src.fetch(context.Background(), "dsg-fringe", false)
	require.NoError(t, err)
	assert.InDelta(t, 34.8, data.Stats.Cpu, 0.001)

	assert.Equal(t, "k", gotKey)
	assert.Equal(t, "/api/v5/query_range", gotPath)
	// The window ends on a step boundary so the newest bucket is complete.
	end := time.Date(2026, 11, 24, 3, 36, 0, 0, time.UTC)
	assert.EqualValues(t, end.UnixMilli(), gotBody["end"])
	assert.EqualValues(t, end.Add(-signozWindow).UnixMilli(), gotBody["start"])
	assert.Equal(t, "scalar", gotBody["requestType"])
}

func TestSignozSourceHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad key", http.StatusUnauthorized)
	}))
	defer srv.Close()
	src := &signozSource{url: srv.URL, key: "k", client: srv.Client(), now: time.Now}
	_, err := src.fetch(context.Background(), "dsg-fringe", false)
	assert.ErrorContains(t, err, "401")
}

func TestSignozSystemsEnv(t *testing.T) {
	assert.Nil(t, newSignozSourceFromEnv(), "unconfigured means disabled")

	t.Setenv("SIGNOZ_URL", "https://signoz.example/")
	t.Setenv("SIGNOZ_API_KEY", "k")
	t.Setenv("SIGNOZ_SYSTEMS", "Fringe = dsg-fringe, Beacon=dsg-beacon,broken,=x,y=")
	src := newSignozSourceFromEnv()
	require.NotNil(t, src)
	assert.Equal(t, "https://signoz.example", src.url)

	host, ok := src.hostFor(" fringe ")
	assert.True(t, ok)
	assert.Equal(t, "dsg-fringe", host)
	_, ok = src.hostFor("Central A")
	assert.False(t, ok, "unlisted systems keep their agent")
	_, ok = (*signozSource)(nil).hostFor("Fringe")
	assert.False(t, ok)
}

func TestFromSignoz(t *testing.T) {
	res, err := parseSignozResponse(fringeFixture(t))
	require.NoError(t, err)
	data, err := buildSignozData(res, "dsg-fringe", false)
	require.NoError(t, err)

	sys := &System{data: data}
	assert.True(t, sys.fromSignoz())
	assert.Contains(t, data.Info.AgentVersion, "-signoz")
	assert.True(t, strings.HasPrefix(data.Info.AgentVersion, beszel.Version), "the UI parses the version before the dash")

	data.Info.AgentVersion = beszel.Version
	assert.False(t, sys.fromSignoz(), "agent data is not SigNoz data")
	assert.False(t, (&System{}).fromSignoz(), "no data yet")
}
