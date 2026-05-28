package sshcmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

type Command struct {
	Argv []string `json:"argv"`
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
