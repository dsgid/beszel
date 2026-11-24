# DSG build of Beszel

This fork carries DSG patches and merges upstream `main`. The hub at
beszel.dsg.id runs an image built by `build-and-push.sh`. The deployed commit,
digest and rollback are recorded in dsgid/infrastructure at
`configs/suite/suite-beszel-hub-ezqlmd/README.md`.

## Patches

- Custom system order. Drag a card or row by its grip on All Systems to reorder
  it. The order is saved per user in `user_settings.settings.systemOrder`, so it
  needs no schema change and stock Beszel ignores it. View, then Sort By, then
  Custom brings the saved order back after sorting by a column.

- SigNoz host stats. A system listed in `SIGNOZ_SYSTEMS` reads CPU (total and per
  core), memory, swap, root disk, disk I/O, network, load, uptime, Docker containers
  and systemd services from SigNoz instead of a beszel-agent. The
  host OpenTelemetry collectors already ship these (see
  `configs/host-metrics` in dsgid/infrastructure). Code: `internal/hub/systems/signoz.go`.
  Unlisted systems keep their agent, and a listed system ignores its agent.

  | Env | Value |
  | --- | --- |
  | `SIGNOZ_URL` | Base URL, for example `https://signoz.dsg.id` |
  | `SIGNOZ_API_KEY` | SigNoz API key with the Viewer role |
  | `SIGNOZ_SYSTEMS` | `System name=host.name` pairs, for example `Fringe=dsg-fringe,Beacon=dsg-beacon` |

  The host collector must export `system.memory.limit` (the machine total). Without it
  the system reads as down with an error that says so.

  Containers need the collector's `docker_stats` receiver and systemd needs its
  `systemd` receiver (both in `configs/host-metrics`). Gaps against an agent: the
  container ports column has no OTel source; a systemd unit's sub state is derived
  from its active state and its CPU peak repeats the current value; units that are
  plainly inactive are left out; stats arrive every 30 s, so there is no realtime
  1 s view.

  Not available from SigNoz, so these stay empty for a listed system: temperatures,
  SMART, ZFS, GPU, fans, battery, extra filesystems, and the network monitors (the
  readyz probes and their loss alerts) that an agent runs.

## Build and push

```sh
git clone --branch <branch> https://github.com/dsgid/beszel /tmp/beszel-build
/tmp/beszel-build/dsg/build-and-push.sh
```

See the script header for what it needs and what it prints. If upstream CI
changes how it builds the web UI before `docker build`, mirror that in the
script.

## Before deploying a newer commit

Upstream `main` runs ahead of the latest release and can add PocketBase
migrations while `beszel.go` still reports the released version. Compare
`internal/migrations` against the deployed commit. If it changed, back up the
hub data first and treat rollback as restoring that backup, not as swapping the
image back.
