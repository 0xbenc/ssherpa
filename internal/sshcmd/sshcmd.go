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
