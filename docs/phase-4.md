# Phase 4: Jump And Proxy

Phase 4 adds route-oriented SSH workflows on top of the Phase 2 direct
runner and Phase 1 inventory.

The Bash Zoo script already supports Jump and Proxy from synthetic picker
rows. The Go port keeps that interaction but also adds explicit commands
so the behavior is testable and scriptable.

## Jump

Interactive:

```sh
ssherpa jump
ssherpa --print
```

From the top-level picker, select `Jump via intermediate hops`.

Non-interactive:

```sh
ssherpa jump --dest prod --hop bastion --print
ssherpa jump --dest prod --hop bastion --hop edge -- -A
ssherpa jump prod bastion edge --print
```

The generated command is:

```text
ssh -J bastion,edge prod
```

Validation:

- destination is required for non-interactive routes;
- at least one hop is required;
- destination cannot be used as a hop;
- duplicate hops are rejected;
- every route alias must exist in the filtered inventory.

## Proxy

Interactive:

```sh
ssherpa proxy
```

From the top-level picker, select `Start SOCKS proxy`.

Non-interactive:

```sh
ssherpa proxy --select prod --print
ssherpa proxy prod --port 1081 --print
ssherpa proxy --select prod --bind 0.0.0.0 --port 1081 -- -v
```

The default proxy command is:

```text
ssh -D 127.0.0.1:1080 -C -N -o ExitOnForwardFailure=yes prod
```

The Go port intentionally does not use Bash Zoo's
`PermitLocalCommand/LocalCommand` success message. `ExitOnForwardFailure`
lets SSH fail if the local forward cannot bind.

## Shared Flags

Both commands support the normal inventory and SSH runner flags:

```text
--all
--filter SUBSTR
--user USER
--config PATH
--print
--exec
--ssh-binary PATH
--no-kitty
--no-color
-- SSH_ARGS...
```

Proxy also supports:

```text
--bind ADDR
--port PORT
--select ALIAS
```

Jump also supports:

```text
--dest ALIAS
--hop ALIAS
```

`--hop` may be repeated. Comma-separated hop values are also accepted.

## Verification

```sh
go test ./...
go run ./cmd/ssherpa jump --dest prod --hop quoted \
  --print --config internal/sshconfig/testdata/matrix/config
go run ./cmd/ssherpa proxy --select prod --port 1080 \
  --print --config internal/sshconfig/testdata/matrix/config
```

Fake SSH execution and exit-code propagation are covered by `internal/cli`
and `internal/sshcmd` tests.

## Known Limits

- Interactive route building reuses the simple Phase 2 picker rather than
  a richer route dashboard.
- Supervised session metadata for jump/proxy routes waits for the session
  phase.
- `authorized_keys` management remains a later phase.
