# Pre-lock manual testing checklist

Every command in sections B–E was **executed and verified on 2026-06-12**
against `pre-lock` @ `498d9b5` (items marked *corrected* had their commands
or expectations fixed during validation — what you see is the working
version). Sections A, F, and G are inherently manual.

Nothing here touches your real `~/.ssh` or state dir — every scripted item
uses throwaway `mktemp` dirs and `--config`/`--state-dir`/`--path` flags.

## Setup (once)

```sh
cd ~/0xbenc/ssherpa && git checkout pre-lock
go build -o /tmp/ssherpa-check ./cmd/ssherpa
```

## A. Daily-driver feel (manual — this is the part only you can judge)

Connect to a real host with `/tmp/ssherpa-check` (your real config is safe —
connecting only reads it):

- [ ] **`Ctrl-^` opens the session map** (that's `Ctrl-6` on a US layout).
      Footer should read `Ctrl-^/q/Esc close ... Ctrl-^x3 panic`.
- [ ] **The mash:** hammer `Ctrl-^` three times fast → instant panic rope,
      no confirmation, back to your shell. (Your beloved flow, new key.)
- [ ] **`Ctrl-^`, `X`, `X`** → confirmed escape rope works as before.
- [ ] **`Ctrl-]` now passes through to the remote** — open `vim` on the
      remote, put the cursor on a tag, press `Ctrl-]`: jump-to-tag works
      (it used to be swallowed). `telnet` escape works again too.
- [ ] **Rebind works:** reconnect with `--overlay-key 'ctrl-]'` → old key
      opens the overlay, `Ctrl-^` passes through, footer shows `Ctrl-]`.
- [ ] **Recording:** `Ctrl-^`, `T` to record, run some commands, `T` to
      pause, exit. Then `session list`, `session log <id>`,
      `session replay <id>`.
- [ ] **Composer** still on `Ctrl-G`; in emacs on the remote, `Ctrl-G`
      keyboard-quit is still intercepted (known, unchanged; `--no-composer`
      frees it).
- [ ] **Overlay file send/receive** (`s` / `v`) against a real host, both
      a fresh path and an existing one (overwrite confirm should default
      to No).


## B. Security guards & CLI contract

### [ ] Dash-alias injection guard (list skips, proxy refuses, no code execution)

```sh
cd /Users/xbenc/0xbenc/ssherpa && go build -o /tmp/ssherpa-check ./cmd/ssherpa
cfg="$(mktemp -d)/config"
cat > "$cfg" <<'EOF'
Host "-oProxyCommand=touch /tmp/ssherpa-pwned"
    HostName evil.example.com

Host ok
    HostName ok.example.com
EOF
rm -f /tmp/ssherpa-pwned
/tmp/ssherpa-check list --config "$cfg"
/tmp/ssherpa-check list --json --config "$cfg" | grep -A4 diagnostics
/tmp/ssherpa-check proxy --select '-oProxyCommand=touch /tmp/ssherpa-pwned' --print --config "$cfg"; echo "exit=$?"
test -e /tmp/ssherpa-pwned && echo PWNED || echo clean
```

**Expect:** list prints only one row: "ok\tok.example.com" (exit 0). list --json has a diagnostics entry: severity "warning", line 1, message 'skipping host alias "-oProxyCommand=touch /tmp/ssherpa-pwned": a name beginning with "-" would be parsed as an ssh option'. proxy prints to stderr: 'ssherpa: refusing destination "-oProxyCommand=touch /tmp/ssherpa-pwned": a name beginning with "-" would be parsed as an ssh option' and exit=1. Final line: "clean" (/tmp/ssherpa-pwned never created).

> The list warning says "skipping host alias ..." while the proxy refusal says "refusing destination ..."; both end with the same 'would be parsed as an ssh option' phrase. Build line in setup is the one-time build; later items assume /tmp/ssherpa-check exists.

### [ ] Passthrough args preserved: ssh argv has no "--", sftp argv keeps "--" before destination

```sh
cfg="$(mktemp -d)/config"
printf 'Host ok\n    HostName ok.example.com\n' > "$cfg"
/tmp/ssherpa-check --print --select ok --config "$cfg" -- -L 8080:localhost:8080
/tmp/ssherpa-check send /etc/hosts ok --remote /tmp/x --print --config "$cfg"
```

**Expect:** First command (exit 0): "[print] ssh -o 'SendEnv=SSHERPA_SESSION_ID SSHERPA_PARENT_SESSION_ID SSHERPA_DEPTH SSHERPA_ROUTE SSHERPA_ORIGIN_HOST' -o ConnectTimeout=10 ok -L 8080:localhost:8080" — no "--" in the argv, and -L comes AFTER the alias "ok". Second command (exit 0): "[print] sftp -b - -o ConnectTimeout=10 -F <cfg path> -- ok" followed by "[batch]" and "put /etc/hosts /tmp/x" — the "--" sits immediately before the destination "ok".

> The ssh print line does not include -F for the --config path (inventory-only), while the sftp line does include -F <cfg>; that is current behavior, not a bug in the checklist sense. Path after -F varies with mktemp.

### [ ] authkeys add rejects newline smuggled inside a quoted command= option

```sh
auth="$(mktemp -d)/authorized_keys"
key='command="echo a
echo b" ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDb7Ccg8MuAtwJl6bsEjuCHWDtiRtivD3c1vzgbG7N1q alice@example'
/tmp/ssherpa-check authkeys add --key "$key" --path "$auth" --yes; echo "exit=$?"
test -e "$auth" && echo FILE_EXISTS || echo FILE_ABSENT
```

**Expect:** stderr: "ssherpa: SSH public key options cannot contain control characters", exit=1, and FILE_ABSENT (nothing was written).

> There is no --options flag on authkeys add (passing one fails with 'unknown authkeys add argument "--options"'); the options field must be embedded at the front of the --key string, as in the setup above. The quote-aware field scanner keeps the newline inside the quoted command= value so validation catches it (internal/authkeys/authkeys.go validateOptions).

### [ ] authkeys delete refuses duplicate-fingerprint entries without --all-matching

```sh
auth="$(mktemp -d)/authorized_keys"
cat > "$auth" <<'EOF'
from="10.0.0.0/8" ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDb7Ccg8MuAtwJl6bsEjuCHWDtiRtivD3c1vzgbG7N1q alice@example
command="uptime" ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDb7Ccg8MuAtwJl6bsEjuCHWDtiRtivD3c1vzgbG7N1q alice@example
EOF
fp="SHA256:HIw5mTiqXNXNO2h1Vh9R81VrAaKPj4DqNvb3oWElxwk"
/tmp/ssherpa-check authkeys list --json --path "$auth" | grep fingerprint
/tmp/ssherpa-check authkeys delete --fingerprint "$fp" --path "$auth" --yes; echo "exit=$?"
/tmp/ssherpa-check authkeys delete --fingerprint "$fp" --all-matching --path "$auth" --yes; echo "exit=$?"
wc -l < "$auth"
```

**Expect:** list --json shows the same fingerprint SHA256:HIw5mTiqXNXNO2h1Vh9R81VrAaKPj4DqNvb3oWElxwk on both entries (lines 1 and 2). First delete: exit=1 with 'ssherpa: fingerprint SHA256:HIw5...xwk matches 2 authorized_keys entries in <path>; pass --all-matching to delete every matching entry:' followed by 'line 1: ssh-ed25519 options=from="10.0.0.0/8" comment=alice@example' and 'line 2: ssh-ed25519 options=command="uptime" comment=alice@example'; file unchanged. Second delete with --all-matching: exit=0, prints '[removed] SHA256:HIw5...xwk in <path>', a '[summary] ... deleted=2 ...' line, a '[backup] <path>.ssherpa-backup.<timestamp>' line, and a '[report]' block with 'changed yes'. wc -l prints 0 (both entries gone).

> Fingerprint can also be read live from authkeys list --json (.keys[0].fingerprint); it is stable for this CI test key. A timestamped .ssherpa-backup file is left next to the authorized_keys file in the temp dir.

### [ ] Help system: overview, per-command help, unknown topic

```sh
/tmp/ssherpa-check help | head -8
/tmp/ssherpa-check help | grep -ci 'phase 10' || true
/tmp/ssherpa-check help check | head -4
/tmp/ssherpa-check help check | grep -- --user
/tmp/ssherpa-check add --help | head -3
/tmp/ssherpa-check help bogus; echo "exit=$?"
```

**Expect:** Overview (exit 0) starts with "Usage:" / "ssherpa [command] [flags]" / "ssherpa [connect-flags] [-- ssh-args...]" then "Available Commands:" listing add, edit, jump, proxy, forward, send, receive, check, incoming, authkeys, theme, session, list, show, version, help; the 'phase 10' grep count is 0. help check shows "ssherpa check ALIAS... [--json] [--timeout 5s] [--icmp-timeout 2s] [--no-icmp]" and the --user grep matches "ssherpa check --filter SUBSTR [--user USER] [--all] [--json]". add --help (exit 0) starts "Usage:" / "ssherpa add --alias NAME --host HOST [--user USER] [--port PORT]". help bogus: stderr 'ssherpa: unknown help topic "bogus"; run "ssherpa help" for the command list', exit=1.

> help check also documents the exit-code contract in prose: "Exits 0 when every check passed and 2 when any check failed."

### [ ] JSON envelopes carry schema_version 1 (list, check, session list)

```sh
cfg="$(mktemp -d)/config"
printf 'Host ok\n    HostName bogus.invalid.ssherpa-test\n' > "$cfg"
st="$(mktemp -d)"
/tmp/ssherpa-check list --json --config "$cfg" | head -2
/tmp/ssherpa-check check ok --no-icmp --timeout 1s --json --config "$cfg" | head -3; echo "exit=${pipestatus[1]}"
/tmp/ssherpa-check session list --json --state-dir "$st"
```

**Expect:** All three JSON documents open with "{" and then "\"schema_version\": 1," as the second line. check --json continues with "ok": false and a results array (the probe against bogus.invalid.ssherpa-test fails fast); its exit code is 2 because a check failed — that is the documented contract, not an error. session list --json on an empty state dir prints schema_version 1, the state_dir path, and "sessions": [] with exit 0.

> ${pipestatus[1]} is zsh syntax; in bash use ${PIPESTATUS[0]}. The check run finishes in about a second thanks to --no-icmp --timeout 1s.

### [ ] list on missing config: exit 0 plus actionable stderr hint

```sh
/tmp/ssherpa-check list --config /nonexistent/cfg; echo "exit=$?"
/tmp/ssherpa-check list --config /nonexistent/cfg 2>&1 >/dev/null   # hint is on stderr
```

**Expect:** exit=0 with empty stdout. stderr: 'ssherpa: no hosts found in /nonexistent/cfg; run "ssherpa add" to create one (hosts with User git are hidden by default; set SSHERPA_IGNORE_USER_GIT=0 to show them)'. The second command confirms the hint goes to stderr, not stdout.

> Hint mentions both "ssherpa add" and SSHERPA_IGNORE_USER_GIT as the candidate required.

### [ ] Theme forward-compat: unknown role key warns on stderr, run still succeeds (connect mode, not list) *(corrected)*

```sh
d="$(mktemp -d)"
cfg="$d/config"; theme="$d/theme.conf"
printf 'Host ok\n    HostName ok.example.com\n' > "$cfg"
printf 'primary = magenta\nfuture_role = bold\n' > "$theme"
/tmp/ssherpa-check --print --select ok --config "$cfg" --theme-file "$theme"; echo "exit=$?"
/tmp/ssherpa-check list --config "$cfg" --theme-file "$theme"; echo "exit=$?"   # list rejects the flag
```

**Expect:** Connect-mode print: stderr warning 'ssherpa: theme config <theme path>: line 2: unknown theme role "future_role" ignored', then stdout '[print] ssh ... ok', exit=0 — the theme loads (primary=magenta applies) and only the unknown key is ignored. The second command shows that list does NOT take this flag: 'ssherpa: unknown flag "--theme-file"', exit=1.

> Candidate said to run 'list --config CFG --theme-file THEME', but --theme-file is a connect-mode/theme-editor flag only; list rejects it with exit 1. The forward-compat warning is emitted once at connect-mode startup (internal/cli/cli.go reportThemeWarnings) on stderr, before the [print] line. Use the --print --select form above so no real ssh runs.

### [ ] Grep-convention exit codes: missing alias and missing config both exit 2

```sh
cfg="$(mktemp -d)/config"
printf 'Host ok\n    HostName ok.example.com\n' > "$cfg"
/tmp/ssherpa-check show nonexistent-alias --config "$cfg"; echo "exit=$?"
/tmp/ssherpa-check check --config /nonexistent/cfg ok; echo "exit=$?"
```

**Expect:** show: stderr 'ssherpa: alias "nonexistent-alias" not found', exit=2. check against a nonexistent config: exit=2 (not 3) — it prints the normal results table with one row: kind=alias, name=ok, status=invalid, ICMP/local_bind "skipped", message "alias not found" (the missing config is treated as an empty inventory rather than a hard error).

> Matches the pre-lock exit-code unification (docs/PRE_LOCK_PROGRESS.md Batch C: "3 gone, grep(1) convention"). The check command finishes instantly since no probe is attempted for an invalid alias.


## C. SSH config mutation (the scary one — it edits configs)

### [ ] Apostrophe value round-trips through edit set and ssh -G

```sh
cfg="$(mktemp -d)"
printf 'Host ok\n  HostName ok.example.com\n' > "$cfg/config"
/tmp/ssherpa-check edit set ok --user "o'brien" --config "$cfg/config" --yes
cat "$cfg/config"
ssh -G -F "$cfg/config" ok | grep '^user '
```

**Expect:** edit set exits 0 and prints '[updated] ok in <CFG path>' plus a '[backup] <CFG>.ssherpa-backup.<timestamp>' line. cat shows the value double-quoted:
  Host ok
    HostName ok.example.com
    User "o'brien"
ssh -G exits 0 (no 'invalid quotes' fatal) and the grep prints exactly: user o'brien

> If you run ssh -G with stdin not attached to a terminal (e.g. inside a script), ssh may also print 'Pseudo-terminal will not be allocated because stdin is not a terminal.' on stderr — harmless, won't appear in an interactive shell.

### [ ] Mixed-case alias edits update the existing stanza in place

```sh
cfg="$(mktemp -d)"
printf 'Host Prod\n  HostName prod.example.com\n' > "$cfg/config"
/tmp/ssherpa-check edit set prod --user alice --config "$cfg/config" --yes
cat "$cfg/config"
grep -c "^Host" "$cfg/config"
```

**Expect:** Exit 0; first output line is '[updated] Prod in <CFG path>' — note it reports the resolved casing 'Prod', not the typed 'prod'. cat shows the original stanza gained the line '  User alice':
  Host Prod
    HostName prod.example.com
    User alice
No new 'Host prod' stanza is appended; grep -c prints: 1

> Resolved-casing in the [updated] line is the WP5 review fix (plan.Aliases) landing as expected.

### [ ] Delete preserves neighboring comment, Include, and next stanza

```sh
cfg="$(mktemp -d)"
cat > "$cfg/config" <<'EOF'
Host a
  HostName a.example.com

# docs for b
Include extra.conf
Host b
  HostName b.example.com
EOF
/tmp/ssherpa-check edit delete a --config "$cfg/config" --yes
cat "$cfg/config"
```

**Expect:** Exit 0; prints '[removed] a in <CFG path>' plus a '[backup] ...' line. cat shows exactly the surviving block — the Host a stanza and its trailing blank line are gone, everything else intact:
  # docs for b
  Include extra.conf
  Host b
    HostName b.example.com

### [ ] Warning when deleting a stanza whose body contains an Include *(corrected)*

```sh
cfg="$(mktemp -d)"
cat > "$cfg/config" <<'EOF'
Host a
  Include extra.conf
  HostName a.example.com
Host b
  HostName b.example.com
EOF
/tmp/ssherpa-check edit delete a --config "$cfg/config" --dry-run
/tmp/ssherpa-check edit delete a --config "$cfg/config" --yes
```

**Expect:** Both runs exit 0 and print on stderr:
  ssherpa: warning: deleting alias "a" also removes "Include extra.conf" (<CFG path>:2)
The dry run additionally prints '[would-removed] a in <CFG path>' and a unified diff on stdout showing the three stanza lines (including the Include) with '-' prefixes; the file is not modified. The --yes run prints '[removed] a ...' + '[backup] ...' and afterwards cat shows only the Host b stanza.

> CORRECTED: the Include must NOT be the last line of the stanza body. With 'Include extra.conf' as the final body line (the original candidate layout), the parser ends the stanza before it: the Include line is hoisted out and preserved (still indented) and NO warning is emitted. That is the known deferred minor in docs/PRE_LOCK_PROGRESS.md ('Include-at-end-of-stanza scope-change warning unevenness'). The working checklist config puts the Include before HostName, as above. Also note the warning text says 'also removes', not literally 'warning: ... Include ... removed', and it appears on stderr in both --dry-run and --yes modes.

### [ ] Multiple IdentityFile lines survive edit set

```sh
cfg="$(mktemp -d)"
cat > "$cfg/config" <<'EOF'
Host a
  HostName a.example.com
  IdentityFile ~/.ssh/id_ed25519
  IdentityFile ~/.ssh/id_rsa
EOF
/tmp/ssherpa-check edit set a --user bob --config "$cfg/config" --yes
cat "$cfg/config"
grep -c "IdentityFile" "$cfg/config"
```

**Expect:** Exit 0; '[updated] a in <CFG path>' + '[backup] ...'. cat shows '  User bob' inserted after HostName and BOTH IdentityFile lines still present in original order:
  Host a
    HostName a.example.com
    User bob
    IdentityFile ~/.ssh/id_ed25519
    IdentityFile ~/.ssh/id_rsa
grep -c prints: 2

### [ ] Delete refused when alias matches multiple casings

```sh
cfg="$(mktemp -d)"
cat > "$cfg/config" <<'EOF'
Host Prod
  HostName one.example.com
Host PROD
  HostName two.example.com
EOF
cp "$cfg/config" "$cfg/before"
/tmp/ssherpa-check edit delete prod --config "$cfg/config" --yes
echo "EXIT:$?"
diff "$cfg/before" "$cfg/config" && echo UNCHANGED
```

**Expect:** Exit code 1, nothing on stdout, and on stderr (names both casings):
  ssherpa: alias "prod" matches multiple stanzas with different casings (Prod, PROD) in <CFG path>; delete each casing explicitly
diff prints nothing and 'UNCHANGED' is echoed — the file is untouched (and no backup file is created).

> This is the WP5 multi-casing delete refusal review fix; deleting 'Prod' or 'PROD' explicitly is the documented workaround.


## D. Session records & transcripts

### [ ] Torn-tail salvage: session log/grep recover complete frames from a truncated .cast

```sh
S="$(mktemp -d)"
mkdir -p "$S/sessions"
cat > "$S/sessions/demo.json" <<'EOF'
{"id":"demo","target_alias":"prod","started_at":"2026-06-01T10:00:00Z","ended_at":"2026-06-01T10:05:00Z","local_pid":0,"runner_mode":"supervised","state_version":1,"transcript":{"format":"asciicast-v2","started_at":"2026-06-01T10:00:00Z","bytes":0,"frames":5}}
EOF
cat > "$S/sessions/demo.cast" <<'EOF'
{"version": 2, "width": 80, "height": 24, "timestamp": 1718000000}
[0.1, "o", "hello world\r\n"]
[0.2, "o", "line two\r\n"]
[0.3, "o", "line three\r\n"]
[0.4, "o", "line four\r\n"]
[0.5, "o", "line five\r\n"]
EOF
/tmp/ssherpa-check session log demo --state-dir "$S"; echo "exit=$?"
python3 -c 'import os,sys; p=sys.argv[1]; open(p,"r+b").truncate(os.path.getsize(p)-4)' "$S/sessions/demo.cast"
/tmp/ssherpa-check session log demo --state-dir "$S"; echo "exit=$?"
/tmp/ssherpa-check session grep demo hello --state-dir "$S"; echo "exit=$?"
```

**Expect:** Before truncation: all five lines ("hello world" ... "line five") on stdout, exit=0, no warning. After truncating 4 bytes off the tail: stderr prints "ssherpa: transcript tail is incomplete; showing 4 frame(s)" and stdout prints the first four lines ending at "line four", exit=0. grep then prints the same stderr warning plus "0:00:1: hello world" on stdout, exit=0.

> The warning is on stderr (confirmed: 2>/dev/null leaves only the transcript lines). Truncating the last 4 bytes leaves the final frame line incomplete, which drops exactly one frame. Record JSON copied from internal/state/state_test.go shape; with transcript.path empty the cast resolves to STATE/sessions/<id>.cast (internal/cli/session.go transcriptPathForRecord).

### [ ] Grep exit convention: 0 match / 1 no-match / 2 bad regex

```sh
G="$(mktemp -d)"
mkdir -p "$G/sessions"
cat > "$G/sessions/demo.json" <<'EOF'
{"id":"demo","target_alias":"prod","started_at":"2026-06-01T10:00:00Z","ended_at":"2026-06-01T10:05:00Z","local_pid":0,"runner_mode":"supervised","state_version":1}
EOF
cat > "$G/sessions/demo.cast" <<'EOF'
{"version": 2, "width": 80, "height": 24, "timestamp": 1718000000}
[0.1, "o", "hello world\r\n"]
[0.2, "o", "goodbye\r\n"]
EOF
/tmp/ssherpa-check session grep demo hello --state-dir "$G"; echo "exit=$?"
/tmp/ssherpa-check session grep demo no-such-text --state-dir "$G"; echo "exit=$?"
/tmp/ssherpa-check session grep demo '[bad' --state-dir "$G"; echo "exit=$?"
```

**Expect:** Match: "0:00:1: hello world" then exit=0. No match: no output at all, exit=1. Bad pattern: stderr "ssherpa: grep transcript: error parsing regexp: missing closing ]: `[bad`", exit=2.

> If you reuse the torn fixture from item 1, the no-match case additionally prints the torn-tail warning on stderr but still exits 1; this item's fixture is clean so output is exactly as quoted.

### [ ] Prune removes record artifacts (.cast/.log) and orphaned casts; --json carries schema_version + artifacts

```sh
P="$(mktemp -d)"
mkdir -p "$P/sessions"
old="$(date -u -v-10d +%Y-%m-%dT%H:%M:%SZ)"
cat > "$P/sessions/old.json" <<EOF
{"id":"old","target_alias":"prod","started_at":"$old","ended_at":"$old","local_pid":0,"runner_mode":"supervised","state_version":1}
EOF
printf '{"version": 2, "width": 80, "height": 24, "timestamp": 1718000000}\n[0.1, "o", "old session\\r\\n"]\n' > "$P/sessions/old.cast"
printf 'plain log sibling\n' > "$P/sessions/old.log"
printf '{"version": 2, "width": 80, "height": 24, "timestamp": 1718000000}\n[0.1, "o", "orphan\\r\\n"]\n' > "$P/sessions/orphan.cast"
touch -t 202505010000 "$P/sessions/orphan.cast"
/tmp/ssherpa-check session prune --older-than 168h --dry-run --json --state-dir "$P"; echo "exit=$?"
/tmp/ssherpa-check session prune --older-than 168h --state-dir "$P"; echo "exit=$?"
ls "$P/sessions"
```

**Expect:** Dry-run JSON: "schema_version": 1, "dry_run": true, a "sessions" array with id "old", and an "artifacts" array listing the full paths of old.cast, old.log, AND orphan.cast; exit=0. Real prune (human output): "removed 1 session record(s)" then a line "old<TAB>ended=...<TAB>target=prod", then "removed 3 transcript artifact(s)" followed by the three artifact paths; exit=0. Final ls of $P/sessions prints nothing (directory empty).

> Orphan reaping keys on file mtime vs the cutoff (state.go PruneRecords second pass) — the touch -t to a months-old mtime is required or the orphan is kept. date -v-10d is the macOS/BSD form (env is darwin); GNU date would use -d '10 days ago'.

### [ ] Raw-record leak closed: list/show/map --json never emit ssh_argv or control_path

```sh
L="$(mktemp -d)"
mkdir -p "$L/sessions"
cat > "$L/sessions/leaky.json" <<'EOF'
{"id":"leaky","target_alias":"prod","ssh_argv":["ssh","-o","ControlMaster=auto","prod"],"control_path":"/tmp/secret-control-sock","started_at":"2026-06-01T10:00:00Z","ended_at":"2026-06-01T10:05:00Z","local_pid":0,"runner_mode":"supervised","state_version":1}
EOF
/tmp/ssherpa-check session list --json --state-dir "$L" | grep -E 'ssh_argv|control_path|schema_version'
/tmp/ssherpa-check session show leaky --json --state-dir "$L" | grep -E 'ssh_argv|control_path|schema_version'
/tmp/ssherpa-check session map --json --all --state-dir "$L" | grep -E 'ssh_argv|control_path|schema_version'
```

**Expect:** Each of the three commands prints exactly one matching line: '"schema_version": 1,' — no ssh_argv and no control_path anywhere, even though both are present in the on-disk record. show's session object contains only sanitized fields (id, depth, target_alias, runner_mode, started_at, ended_at, origin).

> The sanitized projection is sessionJSON in internal/cli/session.go, whose comment states ssh_argv/control_path are deliberately excluded.

### [ ] Stale local reaping: dead-PID record older than 1h is finalized on session list

```sh
T="$(mktemp -d)"
mkdir -p "$T/sessions"
started="$(date -u -v-2H +%Y-%m-%dT%H:%M:%SZ)"
cat > "$T/sessions/stale.json" <<EOF
{"id":"stale","target_alias":"prod","started_at":"$started","local_pid":1048575,"runner_mode":"supervised","state_version":1}
EOF
kill -0 1048575 2>&1 || echo "pid is dead, good"
/tmp/ssherpa-check session list --state-dir "$T"; echo "exit=$?"
/tmp/ssherpa-check session show stale --json --state-dir "$T" | grep -E 'disconnect_reason|ended_at'
```

**Expect:** list prints stderr "ssherpa: finalized 1 stale local session record(s)", then the human listing shows "active: 0  exited: 1  total: 1" with the row under "Exited": "stale  exited  depth=0  target=prod  route=here  started=..." and "health=disconnected: stale_local_session_cleanup"; exit=0. show --json then contains an "ended_at" timestamp (set to cleanup time) and '"disconnect_reason": "stale_local_session_cleanup"'.

> Reap criteria (state.go staleLocalSession): no ended_at, local_pid > 0 and dead, started_at older than StaleLocalSessionTTL = 1h, not imported/inherited/mirror. PID 1048575 exceeds the darwin PID space so kill -0 reliably fails; if it somehow exists on a reader's machine, pick any dead PID. No fabricated exit_code is written.

### [ ] Imported raw-replay guard: replay of an imported transcript refuses without a tty; cleaned log works *(corrected)*

```sh
S1="$(mktemp -d)"; S2="$(mktemp -d)"
mkdir -p "$S1/sessions"
cat > "$S1/sessions/demo.json" <<'EOF'
{"id":"demo","target_alias":"prod","started_at":"2026-06-01T10:00:00Z","ended_at":"2026-06-01T10:05:00Z","local_pid":0,"runner_mode":"supervised","state_version":1,"transcript":{"format":"asciicast-v2","started_at":"2026-06-01T10:00:00Z","bytes":0,"frames":2}}
EOF
cat > "$S1/sessions/demo.cast" <<'EOF'
{"version": 2, "width": 80, "height": 24, "timestamp": 1718000000}
[0.1, "o", "hello world\r\n"]
[0.2, "o", "goodbye\r\n"]
EOF
/tmp/ssherpa-check session bundle export demo --output "$S1/demo-bundle.zip" --state-dir "$S1"
IMPORTED="$(/tmp/ssherpa-check session bundle import "$S1/demo-bundle.zip" --state-dir "$S2" | awk '$1=="imported"{print $5}')"
echo "IMPORTED=$IMPORTED"
/tmp/ssherpa-check session replay "$IMPORTED" --state-dir "$S2" </dev/null; echo "exit=$?"
/tmp/ssherpa-check session log "$IMPORTED" --raw --state-dir "$S2" </dev/null; echo "exit=$?"
/tmp/ssherpa-check session log "$IMPORTED" --state-dir "$S2"; echo "exit=$?"
```

**Expect:** replay with stdin piped: stderr "ssherpa: session replay emits unfiltered terminal bytes from an imported recording" then "ssherpa: refusing imported raw output without an interactive confirmation", exit=1, nothing replayed. session log --raw refuses identically (message names "session log --raw"), exit=1. Cleaned session log (no --raw) prints "hello world" / "goodbye" and exits 0.

> Correction: there is no --raw flag on session replay — 'session replay ID --raw' fails with 'ssherpa: unknown session replay flag "--raw"' (exit 1) before the guard even runs. Replay is inherently the raw path and is what the guard protects (confirmImportedRawEmit, internal/cli/session.go); the cleaned alternative for an imported record is 'session log IMPORTED_ID', which works. Import output line is 'imported session bundle as <ID>'; the awk in setup extracts it. On a real tty the guard instead shows a default-No confirm dialog.

### [ ] Bundle torn warning: export from a torn cast warns, import surfaces a matching warning

```sh
B="$(mktemp -d)"; B2="$(mktemp -d)"
mkdir -p "$B/sessions"
cat > "$B/sessions/demo.json" <<'EOF'
{"id":"demo","target_alias":"prod","started_at":"2026-06-01T10:00:00Z","ended_at":"2026-06-01T10:05:00Z","local_pid":0,"runner_mode":"supervised","state_version":1}
EOF
cat > "$B/sessions/demo.cast" <<'EOF'
{"version": 2, "width": 80, "height": 24, "timestamp": 1718000000}
[0.1, "o", "hello world\r\n"]
[0.2, "o", "line two\r\n"]
[0.3, "o", "line three\r\n"]
[0.4, "o", "line four\r\n"]
[0.5, "o", "line five\r\n"]
EOF
python3 -c 'import os,sys; p=sys.argv[1]; open(p,"r+b").truncate(os.path.getsize(p)-4)' "$B/sessions/demo.cast"
/tmp/ssherpa-check session bundle export demo --output "$B/torn-bundle.zip" --state-dir "$B"; echo "exit=$?"
/tmp/ssherpa-check session bundle import "$B/torn-bundle.zip" --state-dir "$B2"; echo "exit=$?"
```

**Expect:** Export succeeds (exit=0) with stderr "ssherpa: warning: transcript tail is incomplete; bundle carries 4 complete frames" before the normal "exported session bundle to ..." / "source session: demo" / "transcript sha256: ..." lines. Import succeeds (exit=0) with stderr "ssherpa: warning: transcript tail is incomplete; 4 complete frames imported" before "imported session bundle as <new id>" / "origin: imported_other".

> The two warning strings are similar but not identical ("bundle carries N complete frames" on export vs "N complete frames imported" on import) — both flag the torn tail; the bundle manifest records transcript_torn_tail (internal/transcript/transcript.go). Both warnings go to stderr; the commands themselves still exit 0.


## E. Transfers, check, completions

### [ ] Control-char filename rejection (send) *(corrected)*

```sh
cfg="$(mktemp -d)"
printf 'Host ok\n  HostName 127.0.0.1\n  User tester\n' > "$cfg/config"
work="$(mktemp -d)"
printf 'hello\n' > "$work/$(printf 'bad\nname')"
/tmp/ssherpa-check send "$work/$(printf 'bad\nname')" ok --print --config "$cfg/config"; echo "exit=$?"
```

**Expect:** Exit 1, stderr: ssherpa: local path "<tmpdir>/bad\nname" contains control characters, which sftp batch files cannot carry safely; use the in-band session transfer (overlay send) instead  (the path is Go-quoted, so the newline shows as a literal \n in the message)

> The candidate command used a non-existent file, which fails earlier with a stat error ('no such file or directory', also exit 1) before the control-char check fires. send stats the local file first, so you must actually create a file whose name contains a newline (allowed on APFS). With the file present, the rejection from internal/sshcmd/sshcmd.go:335 fires as expected and points at the in-band/overlay path.

### [ ] SFTP argv shape (send --print)

```sh
cfg="$(mktemp -d)"
printf 'Host ok\n  HostName 127.0.0.1\n  User tester\n' > "$cfg/config"
/tmp/ssherpa-check send /etc/hosts ok --remote /tmp/dest --print --config "$cfg/config"; echo "exit=$?"
```

**Expect:** Exit 0. Output:
[print] sftp -b - -o ConnectTimeout=10 -F <cfg>/config -- ok
[batch]
put /etc/hosts /tmp/dest
The argv contains -o ConnectTimeout=10 and a bare -- immediately before the destination alias; the [batch] block shows the put line.

> The -F path is the temp config path (varies per run). -b - means the batch is fed on stdin.

### [ ] check failure message + exit 2 (table and JSON)

```sh
cfg="$(mktemp -d)"
printf 'Host bad\n  HostName definitely-not-resolvable-xyz.invalid\n  User tester\n' > "$cfg/config"
state="$(mktemp -d)"
/tmp/ssherpa-check check bad --no-icmp --timeout 2s --config "$cfg/config" --state-dir "$state"; echo "exit=$?"
/tmp/ssherpa-check check bad --no-icmp --timeout 2s --json --config "$cfg/config" --state-dir "$state"; echo "exit=$?"
```

**Expect:** Both exit 2. Table row: alias  bad  failed  ...  with non-empty MESSAGE column: "ssh: Could not resolve hostname bad: nodename nor servname provided, or not known". JSON result contains "status": "failed", "ssh_exit_code": 255, and "ssh_error": "ssh: Could not resolve hostname bad: nodename nor servname provided, or not known" (same string mirrored in "message"); top-level "ok": false.

> Caveat for the checklist reader: the probe runs plain `ssh -- bad true` WITHOUT -F — the --config flag only controls which aliases are enumerated (base built at internal/cli/check.go:122 via sshcmd.Resolve, no config path). So the resolve error names the alias ('bad'), not the .invalid HostName, and the check only fails because 'bad' is not resolvable via the tester's real SSH setup. Pick an alias name that does not exist in your real ~/.ssh/config or DNS. The MESSAGE backfill is fillCheckMessage (internal/cli/check.go:550), which copies ssh_error into the previously-empty MESSAGE cell.

### [ ] check with non-matching --filter (empty selector)

```sh
cfg="$(mktemp -d)"
printf 'Host ok\n  HostName 127.0.0.1\n  User tester\n' > "$cfg/config"
state="$(mktemp -d)"
/tmp/ssherpa-check check --filter nomatch --config "$cfg/config" --state-dir "$state"; echo "exit=$?"
/tmp/ssherpa-check check --filter nomatch --json --config "$cfg/config" --state-dir "$state"; echo "exit=$?"
```

**Expect:** Current behavior: prints "No checks selected." to stdout and exits 0. JSON variant exits 0 with {"ok": true, ..., "results": []}.

> Recorded as-is per the audit request. The silent empty table is gone — there is now an explicit "No checks selected." message (internal/cli/check.go:567) — but the EXIT CODE IS STILL 0 and JSON still reports ok:true, so at the exit-code level a non-matching filter still passes. Scripts should additionally assert results is non-empty. If the team intended a non-zero exit here, that change has not landed.

### [ ] Completion scripts sanity (bash/zsh/fish) *(corrected)*

```sh
bash -n /Users/xbenc/0xbenc/ssherpa/completions/ssherpa.bash && echo BASH_SYNTAX_OK
zsh -n /Users/xbenc/0xbenc/ssherpa/completions/ssherpa.zsh && echo ZSH_SYNTAX_OK
cd /Users/xbenc/0xbenc/ssherpa/completions && grep -l 'stop-all' ssherpa.bash ssherpa.zsh ssherpa.fish
grep -c 'replay' ssherpa.bash ssherpa.zsh ssherpa.fish
grep -c 'all-matching' ssherpa.bash ssherpa.zsh ssherpa.fish
grep -c 'overlay-key' ssherpa.bash ssherpa.zsh ssherpa.fish
```

**Expect:** bash -n and zsh -n produce no output (exit 0; the echo markers print). grep -l 'stop-all' lists all three files. 'replay' counts: ssherpa.bash:2, ssherpa.zsh:3, ssherpa.fish:1. 'all-matching' and 'overlay-key' each match at least once in all three files (bash:1, zsh:1, fish:1).

> Two corrections: (1) grep for the flag names WITHOUT the leading dashes — fish declares long options as `complete ... -l overlay-key` (ssherpa.fish line 38) and `-l all-matching` (line 127), so a literal '--overlay-key'/'--all-matching' grep reports fish:0 and looks like a false failure (it also requires `--`/-e to keep grep from eating the pattern as an option). (2) fish is not installed on this machine, so `fish --no-execute completions/ssherpa.fish` could not be run; a tester with fish should run it and expect no output/exit 0.

### [ ] --overlay-key flag validation (non-interactive)

```sh
cfg="$(mktemp -d)"
printf 'Host ok\n  HostName 127.0.0.1\n  User tester\n' > "$cfg/config"
state="$(mktemp -d)"
/tmp/ssherpa-check --select ok --config "$cfg/config" --state-dir "$state" --overlay-key ctrl-g --print; echo "exit=$?"
/tmp/ssherpa-check --select ok --config "$cfg/config" --state-dir "$state" --overlay-key ctrl-c --print; echo "exit=$?"
/tmp/ssherpa-check --select ok --config "$cfg/config" --state-dir "$state" --direct --overlay-key 'ctrl-]' --print; echo "exit=$?"
```

**Expect:** All three exit 1 with, in order:
1) "ssherpa: overlay key Ctrl-G conflicts with composer key Ctrl-G; change one of them"
2) "ssherpa: --overlay-key cannot use reserved key Ctrl-C"
3) "ssherpa: --overlay-key requires supervised mode; remove --direct"

> All three messages matched the audit expectations verbatim in substance; quoted strings above are the exact observed stderr lines. Remember to quote ctrl-] so the shell does not interpret the bracket.

### [ ] version output and recv/receive aliases

```sh
/tmp/ssherpa-check recv --help; echo "exit=$?"
/tmp/ssherpa-check help receive; echo "exit=$?"
/tmp/ssherpa-check version; /tmp/ssherpa-check version | wc -l
```

**Expect:** recv --help and help receive print the identical receive usage block (exit 0), starting "Usage:\n  ssherpa receive REMOTE_PATH --select ALIAS [--local LOCAL_PATH] [--force] [--print]" and ending with the line: "ssherpa recv" is an alias for "ssherpa receive". version prints exactly 3 lines; on a dev build: "ssherpa dev" / "commit: none" / "built: unknown" (wc -l reports 3).

> On a release binary the three version lines carry the real tag/commit/date instead of dev/none/unknown, but the 3-line shape holds. Binary built once with: go build -o /tmp/ssherpa-check ./cmd/ssherpa (run from /Users/xbenc/0xbenc/ssherpa).


## F. Real-server checks (manual, need a box you control)

- [ ] **Nested map:** ssherpa → host A, install ssherpa there, ssherpa →
      host B from inside. Local map shows the chain. On a stock sshd the
      remote side is flat — `session show <id> --json` on your machine
      should now carry a `nested_metadata_blocked` event explaining why.
- [ ] **AcceptEnv opt-in:** add `AcceptEnv SSHERPA_*` to the remote
      sshd_config, restart sshd, repeat → remote lineage populates
      (README "Server-side setup" section documents this).
- [ ] **ControlMaster teardown:** connect, exit, then immediately
      `ls /tmp/ssherpa-$(id -u)/cm/` (or `$XDG_RUNTIME_DIR/ssherpa/cm` on
      Linux) → no lingering socket; reconnecting to the same host within
      10 minutes re-authenticates instead of attaching to an orphan.
- [ ] **Forward reconnect:** `forward --select host --local 8080 --remote
      127.0.0.1:80`, kill the ssh child (`pkill -f 'ssh.*host'`) → it
      respawns with backoff; `forward stop` during the backoff window now
      finalizes the record (check `forward list`).
- [ ] **Transfer a real file** both directions; interrupt a `receive`
      mid-flight (Ctrl-C) → the original destination file is intact.

## G. Release-time checks (after you merge/push/tag)

- [ ] CI green including the new jobs: race detector, govulncheck,
      goreleaser-check; weekly schedule visible in the Actions tab.
- [ ] Dependabot PRs appear (gomod weekly, actions weekly).
- [ ] First release: `goreleaser check` passed; release ran tests on
      ubuntu **and** macos before publishing; assets include SBOMs and
      LICENSE inside the tarball (`tar tzf` it).
- [ ] `gh attestation verify ssherpa_*_darwin_arm64.tar.gz -R 0xbenc/ssherpa`
      succeeds.
- [ ] `brew upgrade` path: cask installs, man page renders
      (`man ssherpa` — now covers session/keys/AcceptEnv), completions
      complete `session repl<TAB>` in your shell.
- [ ] Repo settings done: private vulnerability reporting enabled;
      TAP_GITHUB_TOKEN swapped for a fine-grained PAT.

## Known warts confirmed during validation (not regressions)

- `check --filter nomatch` prints "No checks selected." but still
  **exits 0 / `ok: true`** — the exit-code half of the false-healthy
  audit finding did not land. Scripts should assert `results` is
  non-empty. Candidate one-line fix post-review.
- `check` probes run against the **alias name** without `-F`, so
  `--config` affects enumeration only (pre-existing; cousin of the
  `--config`-is-inventory-only finding in the audit's §15.5 notes).
- Include-as-last-line-of-stanza is hoisted out (preserved, unwarned)
  rather than warned — the documented deferred minor.
- `fish --no-execute completions/ssherpa.fish` still needs one run on a
  machine with fish installed.
