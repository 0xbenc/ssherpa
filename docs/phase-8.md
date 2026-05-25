# Phase 8: Latency Watchdog

Phase 8 adds an opt-in local health watchdog for supervised sessions.
It never injects commands into the interactive remote shell. Probes run
as a separate sidecar SSH process and warnings are written to local
stderr plus local session state.

## Usage

Enable warning-only monitoring:

```sh
ssherpa --select prod --latency-warn 2s
ssherpa jump --dest prod --hop bastion --latency-warn 2s
ssherpa proxy --select prod --latency-warn 2s
```

Enable explicit disconnect after sustained unhealthy probes:

```sh
ssherpa --select prod --latency-warn 2s --latency-disconnect 30s
```

`--latency-disconnect` is rejected unless `--latency-warn` is also set.
Latency flags are rejected with `--direct` because the watchdog belongs
to the supervised PTY runner.

## Probe Model

The watchdog builds a sidecar command from the same SSH binary and route
metadata as the supervised connection:

```text
ssh -o BatchMode=yes -o ConnectTimeout=5 [-J hop,hop] target true
```

The sidecar is intentionally separate from the user PTY. A failed probe
or a probe duration above `--latency-warn` records a
`latency_warning` event. A later healthy probe records
`latency_recovered`.

## State

Session records may now include:

```json
{
  "events": [
    {
      "time": "2026-05-24T12:00:00Z",
      "type": "latency_warning",
      "message": "sidecar probe took 5s; threshold 2s",
      "latency_ms": 5000,
      "threshold_ms": 2000
    }
  ],
  "disconnect_reason": "latency unhealthy for 30s; disconnect threshold 30s"
}
```

Inspect events with:

```sh
ssherpa session show SESSION_ID
ssherpa session show SESSION_ID --json
ssherpa session map
```

`session map` and the in-session `Ctrl-]` overlay show active sessions
only. Use `ssherpa session map --all` when you need exited historical
records in the lineage view.

## Safety Rules

- No probe bytes are written to the remote PTY.
- Warning-only monitoring is the default when `--latency-warn` is used.
- Automatic disconnect requires the explicit `--latency-disconnect`
  flag and a sustained unhealthy interval.
- `--direct` disables the supervisor and therefore cannot use the
  watchdog.

## Limits

The sidecar measures SSH reachability and authentication responsiveness,
not keystroke echo latency inside the existing PTY. It can be more
expensive than a passive heuristic because each sample starts a separate
SSH process.
