package sshcmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// DefaultForwardBind is the loopback address used when a `--local`
// spec omits the bind address. The same value the CLI parser
// substitutes when the user types `--local 5432` instead of
// `--local 127.0.0.1:5432`.
const DefaultForwardBind = "127.0.0.1"

type Command struct {
	Argv []string `json:"argv"`
}

type SFTPTransferDirection string

const (
	SFTPTransferSend    SFTPTransferDirection = "send"
	SFTPTransferReceive SFTPTransferDirection = "receive"
)

type SFTPTransfer struct {
	Direction  SFTPTransferDirection
	Alias      string
	Config     string
	LocalPath  string
	RemotePath string
	Batch      string
}

type PrintCommand struct {
	Argv  []string `json:"argv"`
	Alias string   `json:"alias"`
}

type ResolveOptions struct {
	SSHBinary string
	NoKitty   bool
	Env       []string
	LookPath  func(string) (string, error)
}

func Resolve(opts ResolveOptions) Command {
	lookPath := opts.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}

	if opts.SSHBinary != "" {
		return Command{Argv: []string{opts.SSHBinary}}
	}

	if envValue(opts.Env, "SSHERPA_SSH_BINARY") != "" {
		return Command{Argv: []string{envValue(opts.Env, "SSHERPA_SSH_BINARY")}}
	}

	if !opts.NoKitty && !envBool(opts.Env, "SSHERPA_NO_KITTY") && isKitty(opts.Env) {
		if _, err := lookPath("kitten"); err == nil {
			return Command{Argv: []string{"kitten", "ssh"}}
		}
		if _, err := lookPath("kitty"); err == nil {
			return Command{Argv: []string{"kitty", "+kitten", "ssh"}}
		}
	}

	return Command{Argv: []string{"ssh"}}
}

func BuildDirect(base Command, alias string, extraArgs []string) Command {
	argv := append([]string(nil), base.Argv...)
	argv = append(argv, alias)
	argv = append(argv, extraArgs...)
	return Command{Argv: argv}
}

func BuildJump(base Command, destination string, hops []string, extraArgs []string) Command {
	argv := append([]string(nil), base.Argv...)
	argv = append(argv, "-J", strings.Join(hops, ","), destination)
	argv = append(argv, extraArgs...)
	return Command{Argv: argv}
}

func BuildProxy(base Command, alias string, bind string, port int, extraArgs []string) Command {
	argv := append([]string(nil), base.Argv...)
	argv = append(argv,
		"-D", fmt.Sprintf("%s:%d", bind, port),
		"-C",
		"-N",
		"-o", "ExitOnForwardFailure=yes",
		alias,
	)
	argv = append(argv, extraArgs...)
	return Command{Argv: argv}
}

// BuildForward assembles `ssh -L <localBind>:<localPort>:<remoteHost>:<remotePort>`
// for a non-interactive local-port-forward tunnel. If through is non-empty
// the route is prefixed with `-J through` so the forward terminates on
// the alias's target rather than on the jump host. -N suppresses remote
// command execution; ExitOnForwardFailure makes the SSH client fail
// loudly when the forward can't be established (e.g. local port in use)
// instead of silently leaving an unusable session.
func BuildForward(base Command, alias string, localBind string, localPort int, remoteHost string, remotePort int, through string, extraArgs []string) Command {
	argv := append([]string(nil), base.Argv...)
	if through != "" {
		argv = append(argv, "-J", through)
	}
	argv = append(argv,
		"-L", fmt.Sprintf("%s:%d:%s:%d", localBind, localPort, remoteHost, remotePort),
		"-N",
		"-o", "ExitOnForwardFailure=yes",
		alias,
	)
	argv = append(argv, extraArgs...)
	return Command{Argv: argv}
}

func BuildProbe(base Command, alias string, hops []string) Command {
	alias = strings.TrimSpace(alias)
	if alias == "" || len(base.Argv) == 0 {
		return Command{}
	}

	argv := append([]string(nil), base.Argv...)
	argv = append(argv,
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=5",
	)
	if len(hops) > 0 {
		argv = append(argv, "-J", strings.Join(hops, ","))
	}
	argv = append(argv, alias, "true")
	return Command{Argv: argv}
}

func BuildSFTP(binary string, transfer SFTPTransfer) Command {
	if strings.TrimSpace(binary) == "" {
		binary = "sftp"
	}
	argv := []string{binary, "-b", "-"}
	if strings.TrimSpace(transfer.Config) != "" {
		argv = append(argv, "-F", transfer.Config)
	}
	argv = append(argv, transfer.Alias)
	return Command{Argv: argv}
}

func BuildSFTPBatch(transfer SFTPTransfer) string {
	switch transfer.Direction {
	case SFTPTransferReceive:
		return fmt.Sprintf("get %s %s\n", quoteSFTPPath(transfer.RemotePath), quoteSFTPPath(transfer.LocalPath))
	default:
		return fmt.Sprintf("put %s %s\n", quoteSFTPPath(transfer.LocalPath), quoteSFTPPath(transfer.RemotePath))
	}
}

func ValidateSFTPTransfer(transfer SFTPTransfer) error {
	if strings.TrimSpace(transfer.Alias) == "" {
		return errors.New("transfer alias is required")
	}
	if strings.TrimSpace(transfer.LocalPath) == "" {
		return errors.New("transfer local path is required")
	}
	if strings.TrimSpace(transfer.RemotePath) == "" {
		return errors.New("transfer remote path is required")
	}
	switch transfer.Direction {
	case SFTPTransferSend, SFTPTransferReceive:
	default:
		return fmt.Errorf("unknown transfer direction %q", transfer.Direction)
	}
	return nil
}

func ValidateJumpRoute(destination string, hops []string) error {
	destination = strings.TrimSpace(destination)
	if destination == "" {
		return errors.New("jump destination is required")
	}
	if len(hops) == 0 {
		return errors.New("at least one jump hop is required")
	}

	seen := map[string]bool{}
	for _, hop := range hops {
		hop = strings.TrimSpace(hop)
		switch {
		case hop == "":
			return errors.New("jump hop cannot be empty")
		case hop == destination:
			return fmt.Errorf("jump hop %q cannot be the destination", hop)
		case seen[hop]:
			return fmt.Errorf("duplicate jump hop %q", hop)
		}
		seen[hop] = true
	}
	return nil
}

func ValidateProxy(alias string, bind string, port int) error {
	if strings.TrimSpace(alias) == "" {
		return errors.New("proxy alias is required")
	}
	if strings.TrimSpace(bind) == "" {
		return errors.New("proxy bind address is required")
	}
	if port < 1 || port > 65535 {
		return errors.New("proxy port must be an integer from 1 to 65535")
	}
	return nil
}

// ValidateForward checks the pieces of a forward command before
// BuildForward is called. The alias plus the through hop are not
// cross-validated against the SSH inventory here — that's the caller's
// job — but trim+range checks for ports and required strings live in one
// place so callers don't repeat them.
func ValidateForward(alias string, localBind string, localPort int, remoteHost string, remotePort int, through string) error {
	if strings.TrimSpace(alias) == "" {
		return errors.New("forward alias is required")
	}
	if strings.TrimSpace(localBind) == "" {
		return errors.New("forward local bind address is required")
	}
	if localPort < 1 || localPort > 65535 {
		return errors.New("forward local port must be an integer from 1 to 65535")
	}
	if strings.TrimSpace(remoteHost) == "" {
		return errors.New("forward remote host is required")
	}
	if remotePort < 1 || remotePort > 65535 {
		return errors.New("forward remote port must be an integer from 1 to 65535")
	}
	if strings.TrimSpace(through) != through {
		return errors.New("forward through hop cannot have surrounding whitespace")
	}
	if through != "" && through == alias {
		return fmt.Errorf("forward through hop %q cannot be the destination", through)
	}
	return nil
}

// ParseForwardLocal accepts both the bare-port shorthand ("5432") and
// the full `[BIND:]PORT` spelling, including bracketed IPv6
// ("[::1]:5432"). It expands the shorthand to DefaultForwardBind so
// callers always receive a fully-resolved (bind, port) pair.
//
// Lives in sshcmd (not cli) so both the CLI flag parser and the TUI
// builder (internal/ui/forward_builder.go) can validate user input
// against the same rules without a cyclic import.
func ParseForwardLocal(value string) (string, int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", 0, errors.New("forward local cannot be empty")
	}
	if !strings.Contains(value, ":") {
		port, err := strconv.Atoi(value)
		if err != nil || port < 1 || port > 65535 {
			return "", 0, fmt.Errorf("invalid forward local port %q", value)
		}
		return DefaultForwardBind, port, nil
	}
	bind, portStr, err := net.SplitHostPort(value)
	if err != nil {
		return "", 0, fmt.Errorf("invalid forward local %q: %w", value, err)
	}
	if strings.TrimSpace(bind) == "" {
		bind = DefaultForwardBind
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("invalid forward local port %q", portStr)
	}
	return bind, port, nil
}

// ParseForwardRemote accepts host:port (or `[ipv6]:port`). Unlike the
// local side, the remote host is required — there is no analogue to the
// "default to loopback" shorthand because the tunnel's whole point is
// to reach something on the remote side.
func ParseForwardRemote(value string) (string, int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", 0, errors.New("forward remote cannot be empty")
	}
	host, portStr, err := net.SplitHostPort(value)
	if err != nil {
		return "", 0, fmt.Errorf("invalid forward remote %q: %w", value, err)
	}
	if strings.TrimSpace(host) == "" {
		return "", 0, fmt.Errorf("invalid forward remote %q: missing host", value)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("invalid forward remote port %q", portStr)
	}
	return host, port, nil
}

func RunDirect(cmd Command, stdin io.Reader, stdout io.Writer, stderr io.Writer) int {
	if len(cmd.Argv) == 0 {
		fmt.Fprintln(stderr, "ssherpa: empty SSH command")
		return 1
	}

	proc := exec.Command(cmd.Argv[0], cmd.Argv[1:]...)
	proc.Stdin = stdin
	proc.Stdout = stdout
	proc.Stderr = stderr

	err := proc.Run()
	if err == nil {
		return 0
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code := exitErr.ExitCode()
		if code >= 0 {
			return code
		}
		return 1
	}

	fmt.Fprintf(stderr, "ssherpa: run %s: %v\n", QuoteArgv(cmd.Argv), err)
	return 1
}

func QuoteArgv(argv []string) string {
	quoted := make([]string, 0, len(argv))
	for _, arg := range argv {
		quoted = append(quoted, quoteArg(arg))
	}
	return strings.Join(quoted, " ")
}

func WritePrintJSON(w io.Writer, cmd Command, alias string) error {
	return json.NewEncoder(w).Encode(PrintCommand{Argv: cmd.Argv, Alias: alias})
}

func Env() []string {
	return os.Environ()
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(item, prefix))
		}
	}
	return ""
}

func envBool(env []string, key string) bool {
	switch strings.ToLower(envValue(env, key)) {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func isKitty(env []string) bool {
	if envValue(env, "KITTY_WINDOW_ID") != "" || envValue(env, "KITTY_PID") != "" {
		return true
	}
	return strings.HasPrefix(envValue(env, "TERM"), "xterm-kitty")
}

func quoteArg(arg string) string {
	if arg == "" {
		return "''"
	}

	if isSafeShellArg(arg) {
		return arg
	}

	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

func isSafeShellArg(arg string) bool {
	for _, r := range arg {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case strings.ContainsRune("@%_+=:,./-", r):
		default:
			return false
		}
	}
	return true
}

func quoteSFTPPath(path string) string {
	if path == "" {
		return `""`
	}
	if isSafeSFTPPath(path) {
		return path
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`).Replace(path) + `"`
}

func isSafeSFTPPath(path string) bool {
	for _, r := range path {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case strings.ContainsRune("@%_+=:,./~-", r):
		default:
			return false
		}
	}
	return true
}
