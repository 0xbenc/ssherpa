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
	FailPrefix      = "SSHERPA_C_FAIL"
	ProbePrefix     = "SSHERPA_C_PROBE"
	ReadyPrefix     = "SSHERPA_C_READY"
	// sentinelStem is the shared prefix of every sentinel above. Paths
	// may never contain it: the receiver and commit one-liners embed the
	// destination and temp path verbatim, so a path like
	// /tmp/SSHERPA_C_READY.bin would put a literal sentinel back into
	// the typed command and reopen the echo-match hole the quote-split
	// closed.
	sentinelStem = "SSHERPA_C_"
)

// ErrNoCompletion reports that the scanned output holds no complete
// DONE or FAIL sentinel line yet. The session driver treats it as
// "still streaming" via errors.Is and keeps waiting.
var ErrNoCompletion = errors.New("completion sentinel not found")

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
	if strings.Contains(dest, sentinelStem) {
		return SendPlan{}, fmt.Errorf("destination must not contain the transfer sentinel marker %q", sentinelStem)
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
	// Checked after derivation so explicit TempPath and nonce-derived
	// paths are covered alike (sanitizeToken keeps "_" and would let a
	// sentinel-bearing nonce through).
	if strings.Contains(temp, sentinelStem) {
		return SendPlan{}, fmt.Errorf("temp path must not contain the transfer sentinel marker %q", sentinelStem)
	}
	b64Len := Base64Length(opts.Size)
	return SendPlan{
		Destination:     dest,
		TempPath:        temp,
		Size:            opts.Size,
		Base64Length:    b64Len,
		SHA256:          hash,
		ProbeCommand:    ProbeCommand(),
		ReceiverCommand: receiverCommand(temp, dest, hash, b64Len),
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
	return "command -v base64 >/dev/null 2>&1 && { command -v sha256sum >/dev/null 2>&1 || command -v shasum >/dev/null 2>&1 || command -v openssl >/dev/null 2>&1; } && head -c 0 </dev/null >/dev/null 2>&1 && stty -a >/dev/null 2>&1 && printf " + sentinelWord(ProbePrefix, " ok\\n") + " || printf " + sentinelWord(ProbePrefix, " fail\\n")
}

// sentinelWord builds the single-quoted printf format word for a
// sentinel, split into adjacent quoted strings after the shared
// "SSHERPA_" prefix: the word 'SSHERPA_' immediately followed by
// 'C_READY\n'. POSIX shells concatenate the pieces back together, so
// the remote OUTPUT carries the literal sentinel while the typed
// command never does. The session driver scans PTY output for the
// literal sentinel and the remote tty echoes every typed command before
// executing it; without the split the echo itself satisfied the
// matcher, turning the capability probe into a no-op and letting the
// payload stream before remote raw mode was established.
func sentinelWord(sentinel string, suffix string) string {
	return "'SSHERPA_''" + strings.TrimPrefix(sentinel, "SSHERPA_") + suffix + "'"
}

func ResetCommand() string {
	return "stty sane 2>/dev/null"
}

func ParseCompletion(output string, expectedSHA256 string) (bool, error) {
	expected := strings.ToLower(strings.TrimSpace(expectedSHA256))
	lines := strings.Split(output, "\n")
	// The final element is either empty (the output ended at a line
	// break) or a line the PTY is still streaming; only complete lines
	// are parsed so a half-received sentinel can never be misread as a
	// hash mismatch or a truncated failure reason.
	for _, line := range lines[:len(lines)-1] {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case FailPrefix:
			return false, failureError(fields[1], strings.Join(fields[2:], " "), expected)
		case DonePrefix:
			if len(fields) != 3 {
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
	}
	return false, ErrNoCompletion
}

// failureError maps a FailPrefix reason code emitted by the receiver or
// commit one-liner to a specific, immediately-surfaced error. Before
// these existed every remote failure printed an unparseable two-field
// DONE line and degraded into a generic 30-second timeout.
func failureError(reason string, detail string, expected string) error {
	switch reason {
	case "head":
		return fmt.Errorf("remote receiver failed to read the payload (head exit %s); remote temp files removed", detail)
	case "base64":
		return fmt.Errorf("remote base64 decode failed (exit %s); remote temp files removed", detail)
	case "hash":
		return fmt.Errorf("remote sha256 %s did not match local sha256 %s; remote temp file removed", detail, expected)
	case "exists":
		return fmt.Errorf("remote destination already exists; refusing to overwrite (status %s)", detail)
	case "mv":
		return fmt.Errorf("remote rename into destination failed (exit %s); remote temp file removed", detail)
	default:
		return fmt.Errorf("remote transfer failed (%s %s)", reason, detail)
	}
}

// receiverCommand's failure branches each emit a distinct FailPrefix
// reason code ("head", "base64") and remove both temp files (the .b64
// staging file and the decoded temp) so a failed transfer never leaks
// remote artifacts or degrades into a timeout.
func receiverCommand(tempPath string, destination string, expectedSHA256 string, b64Len int64) string {
	tmp := ShellQuote(tempPath)
	encoded := ShellQuote(tempPath + ".b64")
	commit := commitCommand(tempPath, destination, expectedSHA256)
	return fmt.Sprintf("( stty -echo -ixon -icanon 2>/dev/null; printf %s; head -c %d > %s; read_rc=$?; stty sane 2>/dev/null; if [ \"$read_rc\" -eq 0 ]; then if base64 -d < %s > %s 2>/dev/null || base64 -D < %s > %s 2>/dev/null; then rm -f %s; %s; else decode_rc=$?; rm -f %s %s; printf %s \"$decode_rc\"; fi; else rm -f %s %s; printf %s \"$read_rc\"; fi )",
		sentinelWord(ReadyPrefix, "\\n"), b64Len, encoded,
		encoded, tmp, encoded, tmp,
		encoded, commit,
		encoded, tmp, sentinelWord(FailPrefix, " base64 %s\\n"),
		encoded, tmp, sentinelWord(FailPrefix, " head %s\\n"))
}

// commitCommand prints DonePrefix only on success; every failure branch
// removes the temp file and emits a FailPrefix reason code ("hash",
// "exists", "mv") so the local driver can fail fast with a specific
// error instead of waiting out the completion timeout.
func commitCommand(tempPath string, destination string, expectedSHA256 string) string {
	tmp := ShellQuote(tempPath)
	dest := ShellQuote(destination)
	hash := ShellQuote(expectedSHA256)
	return "hash=$(sha256sum " + tmp + " 2>/dev/null | awk '{print $1}'); " +
		"if [ -z \"$hash\" ] && command -v shasum >/dev/null 2>&1; then hash=$(shasum -a 256 " + tmp + " | awk '{print $1}'); fi; " +
		"if [ -z \"$hash\" ] && command -v openssl >/dev/null 2>&1; then hash=$(openssl dgst -sha256 " + tmp + " | awk '{print $NF}'); fi; " +
		"if [ \"$hash\" != " + hash + " ]; then rm -f " + tmp + "; printf " + sentinelWord(FailPrefix, " hash %s\\n") + " \"${hash:-none}\"; " +
		"elif [ -e " + dest + " ] || [ -L " + dest + " ]; then rm -f " + tmp + "; printf " + sentinelWord(FailPrefix, " exists 73\\n") + "; " +
		"elif mv " + tmp + " " + dest + "; then printf " + sentinelWord(DonePrefix, " 0 %s\\n") + " \"$hash\"; " +
		"else mv_rc=$?; rm -f " + tmp + "; printf " + sentinelWord(FailPrefix, " mv %s\\n") + " \"$mv_rc\"; fi; " +
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
