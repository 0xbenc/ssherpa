package sshcmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

const (
	OpenSSHClientInstallHint = "install an OpenSSH client, or pass --ssh-binary PATH / set SSHERPA_SSH_BINARY"
	OpenSFTPInstallHint      = "install an OpenSSH SFTP client, or pass --sftp-binary PATH / set SSHERPA_SFTP_BINARY"
	SSHKeygenInstallHint     = "install OpenSSH ssh-keygen, or pass a valid --ssh-keygen path"
)

type BinaryRequirement struct {
	// Name is the canonical command name users recognize, such as "ssh".
	Name string
	// Role describes why the binary is required, such as "SSH client".
	Role string
	// Program is the exact executable name or path to validate. When
	// ValidateCommandBinary is used, the command's argv[0] fills this in.
	Program string
	// Flag and EnvVar identify where Program came from, so diagnostics can
	// point at the knob the user can fix.
	Flag   string
	EnvVar string
	// Hint is appended to validation errors as actionable remediation.
	Hint string
	// LookPath allows tests to isolate PATH lookup without mutating process
	// environment. Production callers leave it nil.
	LookPath func(string) (string, error)
}

type BinaryError struct {
	Requirement BinaryRequirement
	Problem     string
	Err         error
}

func (e BinaryError) Error() string {
	req := e.Requirement
	role := strings.TrimSpace(req.Role)
	if role == "" {
		role = "command"
	}
	program := strings.TrimSpace(req.Program)
	if program == "" {
		program = strings.TrimSpace(req.Name)
	}
	if program == "" {
		program = "unknown"
	}

	source := binarySource(req)
	prefix := fmt.Sprintf("required %s binary", role)
	switch e.Problem {
	case "empty":
		return appendBinaryHint(fmt.Sprintf("%s%s is empty", prefix, source), req.Hint)
	case "surrounding whitespace":
		return appendBinaryHint(fmt.Sprintf("%s%s %q has surrounding whitespace", prefix, source, req.Program), req.Hint)
	case "not found in PATH":
		return appendBinaryHint(fmt.Sprintf("%s%s %q was not found in PATH", prefix, source, program), req.Hint)
	case "does not exist":
		return appendBinaryHint(fmt.Sprintf("%s%s %q does not exist", prefix, source, program), req.Hint)
	case "is a directory":
		return appendBinaryHint(fmt.Sprintf("%s%s %q is a directory", prefix, source, program), req.Hint)
	case "is not executable":
		return appendBinaryHint(fmt.Sprintf("%s%s %q is not executable", prefix, source, program), req.Hint)
	default:
		if e.Err != nil {
			return appendBinaryHint(fmt.Sprintf("%s%s %q cannot be used: %v", prefix, source, program, e.Err), req.Hint)
		}
		return appendBinaryHint(fmt.Sprintf("%s%s %q cannot be used", prefix, source, program), req.Hint)
	}
}

func (e BinaryError) Unwrap() error {
	return e.Err
}

func ValidateCommandBinary(cmd Command, req BinaryRequirement) error {
	if len(cmd.Argv) == 0 {
		if req.Program == "" {
			req.Program = req.Name
		}
		return BinaryError{Requirement: req, Problem: "empty"}
	}
	req.Program = cmd.Argv[0]
	return ValidateBinary(req)
}

func ValidateBinary(req BinaryRequirement) error {
	program := req.Program
	if program == "" {
		program = req.Name
		req.Program = program
	}
	if strings.TrimSpace(program) == "" {
		return BinaryError{Requirement: req, Problem: "empty"}
	}
	if strings.TrimSpace(program) != program {
		return BinaryError{Requirement: req, Problem: "surrounding whitespace"}
	}

	if hasPathSeparator(program) {
		return validateBinaryPath(req)
	}

	lookPath := req.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if _, err := lookPath(program); err != nil {
		return BinaryError{Requirement: req, Problem: "not found in PATH", Err: err}
	}
	return nil
}

func validateBinaryPath(req BinaryRequirement) error {
	info, err := os.Stat(req.Program)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return BinaryError{Requirement: req, Problem: "does not exist", Err: err}
		}
		return BinaryError{Requirement: req, Err: err}
	}
	if info.IsDir() {
		return BinaryError{Requirement: req, Problem: "is a directory"}
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return BinaryError{Requirement: req, Problem: "is not executable"}
	}
	return nil
}

func hasPathSeparator(value string) bool {
	return strings.Contains(value, "/") || strings.Contains(value, "\\")
}

func binarySource(req BinaryRequirement) string {
	switch {
	case strings.TrimSpace(req.Flag) != "":
		return " from " + strings.TrimSpace(req.Flag)
	case strings.TrimSpace(req.EnvVar) != "":
		return " from " + strings.TrimSpace(req.EnvVar)
	default:
		return ""
	}
}

func appendBinaryHint(message string, hint string) string {
	hint = strings.TrimSpace(hint)
	if hint == "" {
		return message
	}
	return message + "; " + hint
}
