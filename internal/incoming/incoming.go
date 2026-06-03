package incoming

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/0xbenc/ssherpa/internal/fsutil"
)

const (
	StateVersion = 1
	markerTTL    = 24 * time.Hour
)

type Marker struct {
	StateVersion     int       `json:"state_version"`
	User             string    `json:"user,omitempty"`
	UID              int       `json:"uid,omitempty"`
	TTY              string    `json:"tty"`
	SSHClient        string    `json:"ssh_client,omitempty"`
	SSHConnection    string    `json:"ssh_connection,omitempty"`
	ClientIP         string    `json:"client_ip,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	MarkerPID        int       `json:"marker_pid"`
	ParentPID        int       `json:"parent_pid,omitempty"`
	SSHerpaSessionID string    `json:"ssherpa_session_id,omitempty"`
	ParentSessionID  string    `json:"parent_session_id,omitempty"`
	Depth            int       `json:"depth,omitempty"`
	Route            []string  `json:"route,omitempty"`
	OriginHost       string    `json:"origin_host,omitempty"`
}

type WhoEntry struct {
	User     string    `json:"user"`
	TTY      string    `json:"tty"`
	LoginAt  time.Time `json:"login_at,omitempty"`
	Host     string    `json:"host,omitempty"`
	ClientIP string    `json:"client_ip,omitempty"`
	Raw      string    `json:"raw,omitempty"`
}

type Session struct {
	User             string    `json:"user"`
	TTY              string    `json:"tty"`
	LoginAt          time.Time `json:"login_at,omitempty"`
	ClientIP         string    `json:"client_ip,omitempty"`
	Host             string    `json:"host,omitempty"`
	Kind             string    `json:"kind"`
	SSHerpa          bool      `json:"ssherpa"`
	SSHerpaSessionID string    `json:"ssherpa_session_id,omitempty"`
	ParentSessionID  string    `json:"parent_session_id,omitempty"`
	Depth            int       `json:"depth,omitempty"`
	Route            []string  `json:"route,omitempty"`
	OriginHost       string    `json:"origin_host,omitempty"`
	MarkerPID        int       `json:"marker_pid,omitempty"`
	ParentPID        int       `json:"parent_pid,omitempty"`
	CreatedAt        time.Time `json:"created_at,omitempty"`
	StaleMarker      bool      `json:"stale_marker,omitempty"`
	Raw              string    `json:"raw,omitempty"`
}

type Options struct {
	RuntimeDir string
	Env        []string
	Now        func() time.Time
	RunWho     func() ([]byte, error)
	PidAlive   func(int) bool
}

func List(opts Options) ([]Session, error) {
	now := optionNow(opts)
	whoBytes, err := runWho(opts)
	if err != nil {
		return nil, err
	}
	whoEntries := ParseWho(string(whoBytes), now)
	markers, _ := ReadMarkers(opts)
	byTTY := usableMarkersByTTY(markers, now, opts.PidAlive)

	sessions := make([]Session, 0, len(whoEntries))
	for _, entry := range whoEntries {
		session := Session{
			User:     entry.User,
			TTY:      NormalizeTTY(entry.TTY),
			LoginAt:  entry.LoginAt,
			ClientIP: entry.ClientIP,
			Host:     entry.Host,
			Kind:     "ssh",
			Raw:      entry.Raw,
		}
		if marker, ok := byTTY[NormalizeTTY(entry.TTY)]; ok && markerMatches(entry, marker) {
			session.CreatedAt = marker.CreatedAt
			session.MarkerPID = marker.MarkerPID
			session.ParentPID = marker.ParentPID
			session.SSHerpaSessionID = marker.SSHerpaSessionID
			session.ParentSessionID = marker.ParentSessionID
			session.Depth = marker.Depth
			session.Route = append([]string(nil), marker.Route...)
			session.OriginHost = marker.OriginHost
			if marker.SSHerpaSessionID != "" {
				session.SSHerpa = true
				session.Kind = "ssherpa"
			}
		}
		sessions = append(sessions, session)
	}
	sort.SliceStable(sessions, func(i int, j int) bool {
		if !sessions[i].LoginAt.Equal(sessions[j].LoginAt) {
			return sessions[i].LoginAt.Before(sessions[j].LoginAt)
		}
		if sessions[i].TTY != sessions[j].TTY {
			return sessions[i].TTY < sessions[j].TTY
		}
		return sessions[i].User < sessions[j].User
	})
	return sessions, nil
}

func ReadMarkers(opts Options) ([]Marker, error) {
	dir, err := RuntimeDir(opts)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read incoming marker dir: %w", err)
	}
	var markers []Marker
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		var marker Marker
		if err := json.Unmarshal(data, &marker); err != nil {
			continue
		}
		if marker.TTY == "" {
			continue
		}
		marker.TTY = NormalizeTTY(marker.TTY)
		markers = append(markers, marker)
	}
	sort.SliceStable(markers, func(i int, j int) bool {
		return markers[i].CreatedAt.Before(markers[j].CreatedAt)
	})
	return markers, nil
}

func WriteMarker(marker Marker, opts Options) (string, error) {
	dir, err := RuntimeDir(opts)
	if err != nil {
		return "", err
	}
	marker.StateVersion = StateVersion
	marker.TTY = NormalizeTTY(marker.TTY)
	if marker.TTY == "" {
		return "", errors.New("SSH_TTY is required")
	}
	if marker.CreatedAt.IsZero() {
		marker.CreatedAt = optionNow(opts).UTC()
	}
	if marker.MarkerPID == 0 {
		marker.MarkerPID = os.Getpid()
	}
	if marker.UID == 0 {
		marker.UID = os.Getuid()
	}
	if marker.User == "" {
		marker.User = strings.TrimSpace(os.Getenv("USER"))
	}
	path := MarkerPath(dir, marker)
	data, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal incoming marker: %w", err)
	}
	data = append(data, '\n')
	if _, err := fsutil.AtomicWriteFile(path, data, fsutil.WriteOptions{Mode: 0o600}); err != nil {
		return "", err
	}
	return path, nil
}

func RemoveMarker(path string) {
	if path != "" {
		_ = os.Remove(path)
	}
}

func MarkerFromEnv(env []string, parentPID int, now time.Time) Marker {
	return Marker{
		StateVersion:     StateVersion,
		User:             envValue(env, "USER"),
		UID:              os.Getuid(),
		TTY:              NormalizeTTY(envValue(env, "SSH_TTY")),
		SSHClient:        envValue(env, "SSH_CLIENT"),
		SSHConnection:    envValue(env, "SSH_CONNECTION"),
		ClientIP:         clientIP(envValue(env, "SSH_CLIENT"), envValue(env, "SSH_CONNECTION")),
		CreatedAt:        now.UTC(),
		MarkerPID:        os.Getpid(),
		ParentPID:        parentPID,
		SSHerpaSessionID: strings.TrimSpace(envValue(env, "SSHERPA_SESSION_ID")),
		ParentSessionID:  strings.TrimSpace(envValue(env, "SSHERPA_PARENT_SESSION_ID")),
		Depth:            parseInt(envValue(env, "SSHERPA_DEPTH")),
		Route:            splitRoute(envValue(env, "SSHERPA_ROUTE")),
		OriginHost:       strings.TrimSpace(envValue(env, "SSHERPA_ORIGIN_HOST")),
	}
}

func RuntimeDir(opts Options) (string, error) {
	if strings.TrimSpace(opts.RuntimeDir) != "" {
		return filepath.Clean(opts.RuntimeDir), nil
	}
	if value := envValue(opts.Env, "SSHERPA_INCOMING_DIR"); value != "" {
		return filepath.Clean(value), nil
	}
	if value := envValue(opts.Env, "XDG_RUNTIME_DIR"); value != "" {
		return filepath.Join(value, "ssherpa", "incoming"), nil
	}
	if runtime.GOOS != "darwin" {
		uid := os.Getuid()
		path := filepath.Join("/run/user", strconv.Itoa(uid), "ssherpa", "incoming")
		if _, err := os.Stat(filepath.Dir(path)); err == nil {
			return path, nil
		}
	}
	tmp := envValue(opts.Env, "TMPDIR")
	if tmp == "" {
		tmp = os.TempDir()
	}
	return filepath.Join(tmp, fmt.Sprintf("ssherpa-%d", os.Getuid()), "incoming"), nil
}

func MarkerPath(dir string, marker Marker) string {
	tty := strings.ReplaceAll(NormalizeTTY(marker.TTY), "/", "_")
	if tty == "" {
		tty = "unknown"
	}
	pid := marker.MarkerPID
	if pid == 0 {
		pid = os.Getpid()
	}
	return filepath.Join(dir, fmt.Sprintf("%s-%d.json", tty, pid))
}

func ParseWho(text string, now time.Time) []WhoEntry {
	var out []WhoEntry
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		entry := WhoEntry{User: fields[0], TTY: NormalizeTTY(fields[1]), Raw: line}
		if len(fields) >= 4 && strings.Contains(fields[2], "-") {
			if loginAt, ok := parseWhoTime(fields[2], fields[3], now); ok {
				entry.LoginAt = loginAt
			}
		} else if len(fields) >= 5 {
			if loginAt, ok := parseWhoTime(fields[2]+" "+fields[3], fields[4], now); ok {
				entry.LoginAt = loginAt
			}
		}
		if start := strings.LastIndex(line, "("); start >= 0 {
			if end := strings.LastIndex(line, ")"); end > start {
				entry.Host = strings.TrimSpace(line[start+1 : end])
				entry.ClientIP = trimHostPort(entry.Host)
			}
		}
		if entry.TTY == "" || strings.HasPrefix(entry.TTY, "tty") {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func NormalizeTTY(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "/dev/")
	return strings.Trim(value, "/")
}

func ShellHook(shell string) (string, error) {
	shell = strings.ToLower(strings.TrimSpace(shell))
	switch shell {
	case "", "sh", "bash", "zsh":
		return strings.Join([]string{
			`# ssherpa incoming-session marker`,
			`if [ -n "$SSH_TTY" ] && command -v ssherpa >/dev/null 2>&1; then`,
			`  ssherpa incoming mark --watch-parent "$$" --quiet >/dev/null 2>&1 &`,
			`fi`,
			``,
		}, "\n"), nil
	case "fish":
		return strings.Join([]string{
			`# ssherpa incoming-session marker`,
			`if test -n "$SSH_TTY"; and command -v ssherpa >/dev/null 2>&1`,
			`  ssherpa incoming mark --watch-parent $fish_pid --quiet >/dev/null 2>&1 &`,
			`end`,
			``,
		}, "\n"), nil
	default:
		return "", fmt.Errorf("unsupported shell %q", shell)
	}
}

func runWho(opts Options) ([]byte, error) {
	if opts.RunWho != nil {
		return opts.RunWho()
	}
	out, err := exec.Command("who").Output()
	if err != nil {
		return nil, fmt.Errorf("run who: %w", err)
	}
	return out, nil
}

func usableMarkersByTTY(markers []Marker, now time.Time, alive func(int) bool) map[string]Marker {
	out := map[string]Marker{}
	for _, marker := range markers {
		if marker.CreatedAt.IsZero() || now.Sub(marker.CreatedAt) > markerTTL {
			continue
		}
		if marker.MarkerPID > 0 {
			checkAlive := alive
			if checkAlive == nil {
				checkAlive = processAlive
			}
			if !checkAlive(marker.MarkerPID) {
				continue
			}
		}
		tty := NormalizeTTY(marker.TTY)
		current, ok := out[tty]
		if !ok || marker.CreatedAt.After(current.CreatedAt) {
			out[tty] = marker
		}
	}
	return out
}

func markerMatches(entry WhoEntry, marker Marker) bool {
	if NormalizeTTY(entry.TTY) != NormalizeTTY(marker.TTY) {
		return false
	}
	if entry.User != "" && marker.User != "" && entry.User != marker.User {
		return false
	}
	if entry.ClientIP != "" && marker.ClientIP != "" && entry.ClientIP != marker.ClientIP {
		return false
	}
	return true
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, syscall.Signal(0))
	return err == nil || err == syscall.EPERM
}

func optionNow(opts Options) time.Time {
	if opts.Now != nil {
		return opts.Now()
	}
	return time.Now()
}

func parseWhoTime(datePart string, timePart string, now time.Time) (time.Time, bool) {
	layouts := []string{"2006-01-02 15:04", "Jan 2 15:04"}
	value := datePart + " " + timePart
	for _, layout := range layouts {
		parsed, err := time.ParseInLocation(layout, value, time.Local)
		if err != nil {
			continue
		}
		if layout == "Jan 2 15:04" {
			parsed = time.Date(now.Year(), parsed.Month(), parsed.Day(), parsed.Hour(), parsed.Minute(), 0, 0, time.Local)
			if parsed.After(now.Add(24 * time.Hour)) {
				parsed = parsed.AddDate(-1, 0, 0)
			}
		}
		return parsed, true
	}
	return time.Time{}, false
}

func clientIP(sshClient string, sshConnection string) string {
	if sshClient != "" {
		parts := strings.Fields(sshClient)
		if len(parts) > 0 {
			return trimHostPort(parts[0])
		}
	}
	if sshConnection != "" {
		parts := strings.Fields(sshConnection)
		if len(parts) > 0 {
			return trimHostPort(parts[0])
		}
	}
	return ""
}

func trimHostPort(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, "[]")
	if value == "" {
		return ""
	}
	if strings.Count(value, ":") == 1 {
		host, _, ok := strings.Cut(value, ":")
		if ok {
			return host
		}
	}
	return value
}

func splitRoute(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func parseInt(value string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(value))
	return n
}

func envValue(env []string, key string) string {
	if env == nil {
		env = os.Environ()
	}
	prefix := key + "="
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			return strings.TrimPrefix(item, prefix)
		}
	}
	return ""
}
