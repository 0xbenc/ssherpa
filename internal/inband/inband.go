package inband

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	DefaultMaxBytes = 5 * 1024 * 1024
	DonePrefix      = "SSHERPA_C_DONE"
	ProbePrefix     = "SSHERPA_C_PROBE"
)

type SendOptions struct {
	Destination string
	TempPath    string
	Size        int64
	SHA256      string
	MaxBytes    int64
	Nonce       string
}

type SendPlan struct {
	Destination     string
	TempPath        string
	Size            int64
	Base64Length    int64
	SHA256          string
	ProbeCommand    string
	ReceiverCommand string
	CommitCommand   string
	ResetCommand    string
}

func NewSendPlan(opts SendOptions) (SendPlan, error) {
	dest := strings.TrimSpace(opts.Destination)
	if dest == "" {
		return SendPlan{}, errors.New("destination is required")
	}
	if opts.Size < 0 {
		return SendPlan{}, errors.New("size cannot be negative")
	}
	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if opts.Size > maxBytes {
		return SendPlan{}, fmt.Errorf("payload is %d bytes; max in-band size is %d bytes", opts.Size, maxBytes)
	}
	hash := strings.ToLower(strings.TrimSpace(opts.SHA256))
	if len(hash) != sha256.Size*2 || !isLowerHex(hash) {
		return SendPlan{}, errors.New("sha256 must be a 64-character hex digest")
	}
	temp := strings.TrimSpace(opts.TempPath)
	if temp == "" {
		nonce := sanitizeToken(opts.Nonce)
		if nonce == "" {
			nonce = "transfer"
		}
		temp = dest + ".ssherpa." + nonce + ".tmp"
	}
	b64Len := Base64Length(opts.Size)
	return SendPlan{
		Destination:     dest,
		TempPath:        temp,
		Size:            opts.Size,
		Base64Length:    b64Len,
		SHA256:          hash,
		ProbeCommand:    ProbeCommand(),
		ReceiverCommand: receiverCommand(temp, b64Len),
		CommitCommand:   commitCommand(temp, dest, hash),
		ResetCommand:    ResetCommand(),
	}, nil
}

func NewSendPlanFromReader(destination string, nonce string, r io.Reader, maxBytes int64) (SendPlan, []byte, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	data, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return SendPlan{}, nil, err
	}
	sum := sha256.Sum256(data)
	plan, err := NewSendPlan(SendOptions{
		Destination: destination,
		Size:        int64(len(data)),
		SHA256:      hex.EncodeToString(sum[:]),
		MaxBytes:    maxBytes,
		Nonce:       nonce,
	})
	if err != nil {
		return SendPlan{}, nil, err
	}
	return plan, data, nil
}

func Base64Length(size int64) int64 {
	if size <= 0 {
		return 0
	}
	return ((size + 2) / 3) * 4
}

func ProbeCommand() string {
	return "command -v base64 >/dev/null 2>&1 && { command -v sha256sum >/dev/null 2>&1 || command -v shasum >/dev/null 2>&1 || command -v openssl >/dev/null 2>&1; } && stty -a >/dev/null 2>&1 && printf '" + ProbePrefix + " ok\\n' || printf '" + ProbePrefix + " fail\\n'"
}

func ResetCommand() string {
	return "stty sane 2>/dev/null"
}

func ParseCompletion(output string, expectedSHA256 string) (bool, error) {
	expected := strings.ToLower(strings.TrimSpace(expectedSHA256))
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) != 3 || fields[0] != DonePrefix {
			continue
		}
		if fields[1] != "0" {
			return false, fmt.Errorf("remote commit exited with status %s", fields[1])
		}
		got := strings.ToLower(fields[2])
		if got != expected {
			return false, fmt.Errorf("remote sha256 %s did not match local sha256 %s", got, expected)
		}
		return true, nil
	}
	return false, errors.New("completion sentinel not found")
}

func receiverCommand(tempPath string, b64Len int64) string {
	tmp := ShellQuote(tempPath)
	return fmt.Sprintf("( stty -echo -ixon -icanon 2>/dev/null; head -c %d | base64 -d > %s; rc=$?; stty sane 2>/dev/null; exit $rc )", b64Len, tmp)
}

func commitCommand(tempPath string, destination string, expectedSHA256 string) string {
	tmp := ShellQuote(tempPath)
	dest := ShellQuote(destination)
	hash := ShellQuote(expectedSHA256)
	return "hash=$(sha256sum " + tmp + " 2>/dev/null | awk '{print $1}'); " +
		"if [ -z \"$hash\" ] && command -v shasum >/dev/null 2>&1; then hash=$(shasum -a 256 " + tmp + " | awk '{print $1}'); fi; " +
		"if [ -z \"$hash\" ] && command -v openssl >/dev/null 2>&1; then hash=$(openssl dgst -sha256 " + tmp + " | awk '{print $NF}'); fi; " +
		"if [ \"$hash\" = " + hash + " ]; then mv " + tmp + " " + dest + "; rc=$?; else rc=1; fi; " +
		"printf '" + DonePrefix + " %s %s\\n' \"$rc\" \"$hash\"; " +
		"stty sane 2>/dev/null"
}

func ShellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func isLowerHex(value string) bool {
	for _, r := range value {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') {
			continue
		}
		return false
	}
	return true
}

func sanitizeToken(value string) string {
	value = strings.TrimSpace(value)
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		}
	}
	return b.String()
}
