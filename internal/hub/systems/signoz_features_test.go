package systems

import (
	"testing"

	"github.com/henrygd/beszel/internal/entities/container"
	"github.com/henrygd/beszel/internal/entities/systemd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func row(value float64, kv ...string) signozRow {
	labels := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		labels[kv[i]] = kv[i+1]
	}
	return signozRow{labels: labels, value: value}
}

func TestCoreUsage(t *testing.T) {
	rows := []signozRow{
		row(0.5, "cpu", "cpu10", "state", "idle"),
		row(0.1, "cpu", "cpu10", "state", "wait"),
		row(0.9, "cpu", "cpu2", "state", "idle"),
		row(0.0, "cpu", "cpu2", "state", "wait"),
		row(0.7, "cpu", "cpu2", "state", "user"), // ignored
	}
	assert.Equal(t, []uint8{10, 40}, []uint8(coreUsage(rows)))
}

func TestDockerUptime(t *testing.T) {
	assert.Equal(t, "Up 7 days", dockerUptime(7*86400+5))
	assert.Equal(t, "Up 1 day", dockerUptime(86400))
	assert.Equal(t, "Up 3 hours", dockerUptime(3*3600+59))
	assert.Equal(t, "Up 5 minutes", dockerUptime(300))
	assert.Equal(t, "Up 30 seconds", dockerUptime(30))
}

func TestBuildContainers(t *testing.T) {
	res := signozResult{
		"cup": {
			row(604800, "container.name", "web.1.abc", "container.id", "0123456789abcdef0123", "container.image.name", "nginx:1"),
			row(60, "container.name", "api", "container.id", "fedcba9876543210", "container.image.name", "api:2"),
		},
		"ccpu":    {row(12.345e6, "container.name", "web.1.abc")},
		"cmem":    {row(5*(1<<20), "container.name", "web.1.abc")},
		"ctx":     {row(100, "container.name", "web.1.abc")},
		"crx":     {row(200, "container.name", "web.1.abc")},
		"chealth": {row(0, "container.name", "api", "container.state.health.state", "starting"), row(1, "container.name", "api", "container.state.health.state", "healthy")},
	}
	got := buildContainers(res)
	require.Len(t, got, 2)
	assert.Equal(t, "api", got[0].Name) // sorted by name
	assert.Equal(t, container.DockerHealthHealthy, got[0].Health)
	web := got[1]
	assert.Equal(t, "0123456789ab", web.Id)
	assert.Equal(t, "nginx:1", web.Image)
	assert.Equal(t, "Up 7 days", web.Status)
	assert.Equal(t, 1.23, web.Cpu)
	assert.Equal(t, 5.0, web.Mem)
	assert.Equal(t, [2]uint64{100, 200}, web.Bandwidth)
	assert.Empty(t, web.Ports)
	assert.Equal(t, container.DockerHealthNone, web.Health)
}

func TestBuildSystemdServices(t *testing.T) {
	res := signozResult{
		"ustate": {
			row(1, "systemd.unit.name", "ssh.service", "systemd.unit.active_state", "active"),
			row(0, "systemd.unit.name", "ssh.service", "systemd.unit.active_state", "failed"),
			row(1, "systemd.unit.name", "idle.service", "systemd.unit.active_state", "inactive"),
			row(1, "systemd.unit.name", "bad.service", "systemd.unit.active_state", "failed"),
		},
		"ucpu":     {row(500000, "systemd.unit.name", "ssh.service")}, // 0.5 CPU seconds per second
		"umem":     {row(1000, "systemd.unit.name", "ssh.service")},
		"umempeak": {row(4000, "systemd.unit.name", "ssh.service")},
	}
	got, fresh := buildSystemdServices(res)
	assert.True(t, fresh)
	require.Len(t, got, 2)
	assert.Equal(t, "bad", got[0].Name)
	assert.Equal(t, systemd.StatusFailed, got[0].State)
	assert.Equal(t, systemd.SubStateFailed, got[0].Sub)
	ssh := got[1]
	assert.Equal(t, systemd.StatusActive, ssh.State)
	assert.Equal(t, systemd.SubStateRunning, ssh.Sub)
	assert.Equal(t, 50.0, ssh.Cpu)
	assert.Equal(t, 50.0, ssh.CpuPeak)
	assert.Equal(t, uint64(1000), ssh.Mem)
	assert.Equal(t, uint64(4000), ssh.MemPeak)

	none, fresh := buildSystemdServices(signozResult{})
	assert.Nil(t, none)
	assert.False(t, fresh)
}
