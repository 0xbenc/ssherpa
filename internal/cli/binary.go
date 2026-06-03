package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/0xbenc/ssherpa/internal/sshcmd"
)

func sshBinaryRequirement(sshBinary string) sshcmd.BinaryRequirement {
	req := sshcmd.BinaryRequirement{
		Name: "ssh",
		Role: "SSH client",
		Hint: sshcmd.OpenSSHClientInstallHint,
	}
	if strings.TrimSpace(sshBinary) != "" {
		req.Flag = "--ssh-binary"
		return req
	}
	if strings.TrimSpace(os.Getenv("SSHERPA_SSH_BINARY")) != "" {
		req.EnvVar = "SSHERPA_SSH_BINARY"
	}
	return req
}

func sftpBinaryRequirement(flags transferFlags) sshcmd.BinaryRequirement {
	req := sshcmd.BinaryRequirement{
		Name: "sftp",
		Role: "SFTP client",
		Hint: sshcmd.OpenSFTPInstallHint,
	}
	if strings.TrimSpace(flags.SFTPBinary) != "" {
		req.Flag = "--sftp-binary"
		return req
	}
	if strings.TrimSpace(os.Getenv("SSHERPA_SFTP_BINARY")) != "" {
		req.EnvVar = "SSHERPA_SFTP_BINARY"
	}
	return req
}

func sshKeygenBinaryRequirement(path string) sshcmd.BinaryRequirement {
	return sshcmd.BinaryRequirement{
		Name:    "ssh-keygen",
		Role:    "ssh-keygen",
		Program: path,
		Flag:    "--ssh-keygen",
		Hint:    sshcmd.SSHKeygenInstallHint,
	}
}

func validateSSHCommandBinary(cmd sshcmd.Command, sshBinary string, stderr io.Writer) bool {
	if err := sshcmd.ValidateCommandBinary(cmd, sshBinaryRequirement(sshBinary)); err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return false
	}
	return true
}

func validateSFTPCommandBinary(cmd sshcmd.Command, flags transferFlags, stderr io.Writer) bool {
	if err := sshcmd.ValidateCommandBinary(cmd, sftpBinaryRequirement(flags)); err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return false
	}
	return true
}

func validateExplicitSSHKeygen(flags authkeysFlags, stderr io.Writer) bool {
	if strings.TrimSpace(flags.SSHKeygenPath) == "" {
		return true
	}
	if err := sshcmd.ValidateBinary(sshKeygenBinaryRequirement(flags.SSHKeygenPath)); err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return false
	}
	return true
}

func requireBinaryFlagValue(value string, flag string, stderr io.Writer) (string, bool) {
	if strings.TrimSpace(value) == "" {
		fmt.Fprintf(stderr, "ssherpa: %s cannot be empty\n", flag)
		return "", false
	}
	return value, true
}
