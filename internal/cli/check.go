package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/0xbenc/ssherpa/internal/hostlist"
	"github.com/0xbenc/ssherpa/internal/sshcmd"
	"github.com/0xbenc/ssherpa/internal/state"
)

type checkFlags struct {
	inventoryFlags
	StateDir      string
	SSHBinary     string
	Timeout       time.Duration
	ICMPTimeout   time.Duration
	NoICMP        bool
	SavedForward  string
	SavedForwards bool
	Positional    []string
}

type checkOutput struct {
	OK        bool          `json:"ok"`
	CheckedAt time.Time     `json:"checked_at"`
	Results   []checkResult `json:"results"`
}

type checkResult struct {
	Kind            string `json:"kind"`
	Name            string `json:"name"`
	SSHAlias        string `json:"ssh_alias,omitempty"`
	Status          string `json:"status"`
	SSHRttMillis    int64  `json:"ssh_rtt_ms,omitempty"`
	SSHExitCode     int    `json:"ssh_exit_code,omitempty"`
	SSHError        string `json:"ssh_error,omitempty"`
	ICMPStatus      string `json:"icmp_status"`
	ICMPRttMillis   int64  `json:"icmp_rtt_ms,omitempty"`
	LocalBindStatus string `json:"local_bind_status,omitempty"`
	Message         string `json:"message,omitempty"`
}

type sshProbeResult struct {
	Duration time.Duration
	ExitCode int
	Err      error
}

type icmpProbeResult struct {
	Duration time.Duration
	Status   string
}

var runSSHCheckProbe = defaultRunSSHCheckProbe
var runICMPCheckProbe = defaultRunICMPCheckProbe
var runLocalBindCheck = defaultLocalBindCheck

func runCheck(args []string, stdout io.Writer, stderr io.Writer) int {
	if hasHelpFlag(args) {
		printUsage(stdout)
		return 0
	}
	flags, ok := parseCheckFlags(args, stderr)
	if !ok {
		return 1
	}
	if flags.Timeout <= 0 {
		flags.Timeout = 5 * time.Second
	}
	if flags.ICMPTimeout <= 0 {
		flags.ICMPTimeout = 2 * time.Second
	}
	if len(flags.Positional) == 0 && flags.Filter == "" && flags.User == "" && flags.SavedForward == "" && !flags.SavedForwards {
		fmt.Fprintln(stderr, "ssherpa: check requires ALIAS..., --filter, --user, --saved-forward, or --saved-forwards")
		return 1
	}

	out, code := runCheckWithFlags(flags, stderr)
	if code != 0 {
		return code
	}
	if flags.JSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
	} else {
		printCheckTable(stdout, out.Results)
	}
	if out.OK {
		return 0
	}
	return 2
}

func runCheckWithFlags(flags checkFlags, stderr io.Writer) (checkOutput, int) {
	_, inventory, err := loadInventory(flags.inventoryFlags)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return checkOutput{}, 3
	}

	results := []checkResult{}
	base := sshcmd.Resolve(sshcmd.ResolveOptions{SSHBinary: flags.SSHBinary, NoKitty: true, Env: sshcmd.Env()})
	sshBinaryErr := sshcmd.ValidateCommandBinary(base, sshBinaryRequirement(flags.SSHBinary))
	if len(flags.Positional) > 0 {
		for _, name := range flags.Positional {
			alias := findAlias(inventory.Aliases, name)
			if alias == nil {
				results = append(results, checkResult{Kind: "alias", Name: name, SSHAlias: name, Status: "invalid", ICMPStatus: "skipped", LocalBindStatus: "skipped", Message: "alias not found"})
				continue
			}
			results = append(results, checkAlias(*alias, base, flags, sshBinaryErr))
		}
	} else if flags.Filter != "" || flags.User != "" {
		for _, alias := range inventory.Aliases {
			results = append(results, checkAlias(alias, base, flags, sshBinaryErr))
		}
	}
	if flags.SavedForward != "" || flags.SavedForwards {
		savedResults, code := checkSavedForwards(flags, inventory, base, sshBinaryErr, stderr)
		if code != 0 {
			return checkOutput{}, code
		}
		results = append(results, savedResults...)
	}

	out := checkOutput{CheckedAt: time.Now().UTC(), Results: results}
	out.OK = true
	for _, result := range results {
		if result.Status != "ok" {
			out.OK = false
			break
		}
	}
	return out, 0
}

func parseCheckFlags(args []string, stderr io.Writer) (checkFlags, bool) {
	flags := checkFlags{Timeout: 5 * time.Second, ICMPTimeout: 2 * time.Second}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			flags.JSON = true
		case arg == "--all":
			flags.All = true
		case arg == "--filter":
			value, ok := nextArg(args, &i, stderr, "--filter")
			if !ok {
				return flags, false
			}
			flags.Filter = value
		case strings.HasPrefix(arg, "--filter="):
			flags.Filter = strings.TrimPrefix(arg, "--filter=")
		case arg == "--user":
			value, ok := nextArg(args, &i, stderr, "--user")
			if !ok {
				return flags, false
			}
			flags.User = value
		case strings.HasPrefix(arg, "--user="):
			flags.User = strings.TrimPrefix(arg, "--user=")
		case arg == "--config":
			value, ok := nextArg(args, &i, stderr, "--config")
			if !ok {
				return flags, false
			}
			flags.Config = value
		case strings.HasPrefix(arg, "--config="):
			flags.Config = strings.TrimPrefix(arg, "--config=")
		case arg == "--state-dir":
			value, ok := nextArg(args, &i, stderr, "--state-dir")
			if !ok {
				return flags, false
			}
			flags.StateDir = value
		case strings.HasPrefix(arg, "--state-dir="):
			flags.StateDir = strings.TrimPrefix(arg, "--state-dir=")
		case arg == "--ssh-binary":
			value, ok := nextArg(args, &i, stderr, "--ssh-binary")
			if !ok {
				return flags, false
			}
			value, ok = requireBinaryFlagValue(value, "--ssh-binary", stderr)
			if !ok {
				return flags, false
			}
			flags.SSHBinary = value
		case strings.HasPrefix(arg, "--ssh-binary="):
			value, ok := requireBinaryFlagValue(strings.TrimPrefix(arg, "--ssh-binary="), "--ssh-binary", stderr)
			if !ok {
				return flags, false
			}
			flags.SSHBinary = value
		case arg == "--timeout":
			value, ok := nextArg(args, &i, stderr, "--timeout")
			if !ok {
				return flags, false
			}
			d, ok := parseDuration(value, stderr, "--timeout")
			if !ok {
				return flags, false
			}
			flags.Timeout = d
		case strings.HasPrefix(arg, "--timeout="):
			d, ok := parseDuration(strings.TrimPrefix(arg, "--timeout="), stderr, "--timeout")
			if !ok {
				return flags, false
			}
			flags.Timeout = d
		case arg == "--icmp-timeout":
			value, ok := nextArg(args, &i, stderr, "--icmp-timeout")
			if !ok {
				return flags, false
			}
			d, ok := parseDuration(value, stderr, "--icmp-timeout")
			if !ok {
				return flags, false
			}
			flags.ICMPTimeout = d
		case strings.HasPrefix(arg, "--icmp-timeout="):
			d, ok := parseDuration(strings.TrimPrefix(arg, "--icmp-timeout="), stderr, "--icmp-timeout")
			if !ok {
				return flags, false
			}
			flags.ICMPTimeout = d
		case arg == "--no-icmp":
			flags.NoICMP = true
		case arg == "--saved-forward":
			value, ok := nextArg(args, &i, stderr, "--saved-forward")
			if !ok {
				return flags, false
			}
			flags.SavedForward = value
		case strings.HasPrefix(arg, "--saved-forward="):
			flags.SavedForward = strings.TrimPrefix(arg, "--saved-forward=")
		case arg == "--saved-forwards":
			flags.SavedForwards = true
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(stderr, "ssherpa: unknown check flag %q\n", arg)
			return flags, false
		default:
			flags.Positional = append(flags.Positional, arg)
		}
	}
	if flags.SavedForward != "" && flags.SavedForwards {
		fmt.Fprintln(stderr, "ssherpa: --saved-forward and --saved-forwards are mutually exclusive")
		return flags, false
	}
	return flags, true
}

func checkAlias(alias hostlist.Alias, base sshcmd.Command, flags checkFlags, sshBinaryErr error) checkResult {
	result := checkResult{
		Kind:            "alias",
		Name:            alias.Name,
		SSHAlias:        alias.Name,
		ICMPStatus:      "skipped",
		LocalBindStatus: "skipped",
	}
	if sshBinaryErr != nil {
		result.Status = "failed"
		result.SSHExitCode = 1
		result.SSHError = sshBinaryErr.Error()
		result.Message = sshBinaryErr.Error()
	} else {
		cmd := buildCheckProbe(base, alias.Name, nil, flags.Timeout)
		probe := runSSHCheckProbe(cmd, flags.Timeout)
		result.SSHRttMillis = probe.Duration.Milliseconds()
		result.SSHExitCode = probe.ExitCode
		if probe.Err != nil || probe.ExitCode != 0 {
			result.Status = "failed"
			result.SSHError = checkErrString(probe.Err, probe.ExitCode)
		} else {
			result.Status = "ok"
		}
	}
	if flags.NoICMP {
		result.ICMPStatus = "skipped"
		return result
	}
	host := alias.HostName
	if host == "" {
		host = alias.Name
	}
	icmp := runICMPCheckProbe(host, flags.ICMPTimeout)
	result.ICMPStatus = icmp.Status
	result.ICMPRttMillis = icmp.Duration.Milliseconds()
	return result
}

func checkSavedForwards(flags checkFlags, inventory hostlist.Inventory, base sshcmd.Command, sshBinaryErr error, stderr io.Writer) ([]checkResult, int) {
	stateDir, err := state.ResolveDir(flags.StateDir)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: resolve state directory: %v\n", err)
		return nil, 3
	}
	var specs []state.StoredForward
	if flags.SavedForward != "" {
		spec, err := state.ReadForward(stateDir, flags.SavedForward)
		if err != nil {
			return []checkResult{{Kind: "saved_forward", Name: flags.SavedForward, Status: "invalid", ICMPStatus: "skipped", LocalBindStatus: "skipped", Message: err.Error()}}, 0
		}
		specs = []state.StoredForward{spec}
	} else {
		var err error
		specs, err = state.ListForwards(stateDir)
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: list saved forwards: %v\n", err)
			return nil, 3
		}
	}
	results := make([]checkResult, 0, len(specs))
	for _, spec := range specs {
		results = append(results, checkSavedForward(spec, inventory, base, flags, sshBinaryErr))
	}
	return results, 0
}

func checkSavedForward(spec state.StoredForward, inventory hostlist.Inventory, base sshcmd.Command, flags checkFlags, sshBinaryErr error) checkResult {
	result := checkResult{
		Kind:       "saved_forward",
		Name:       spec.Name,
		SSHAlias:   spec.SSHAlias,
		ICMPStatus: "skipped",
	}
	if err := validateStoredForward(spec); err != nil {
		result.Status = "invalid"
		result.LocalBindStatus = "skipped"
		result.Message = err.Error()
		return result
	}
	alias := findAlias(inventory.Aliases, spec.SSHAlias)
	if alias == nil {
		result.Status = "invalid"
		result.LocalBindStatus = "skipped"
		result.Message = fmt.Sprintf("alias %q not found", spec.SSHAlias)
		return result
	}
	if spec.Through != "" && findAlias(inventory.Aliases, spec.Through) == nil {
		result.Status = "invalid"
		result.LocalBindStatus = "skipped"
		result.Message = fmt.Sprintf("alias %q not found", spec.Through)
		return result
	}
	result.LocalBindStatus = runLocalBindCheck(spec.LocalBind, spec.LocalPort)
	hops := []string(nil)
	if spec.Through != "" {
		hops = []string{spec.Through}
	}
	if sshBinaryErr != nil {
		result.Status = "failed"
		result.SSHExitCode = 1
		result.SSHError = sshBinaryErr.Error()
		result.Message = sshBinaryErr.Error()
	} else {
		probe := runSSHCheckProbe(buildCheckProbe(base, spec.SSHAlias, hops, flags.Timeout), flags.Timeout)
		result.SSHRttMillis = probe.Duration.Milliseconds()
		result.SSHExitCode = probe.ExitCode
		if probe.Err != nil || probe.ExitCode != 0 {
			result.Status = "failed"
			result.SSHError = checkErrString(probe.Err, probe.ExitCode)
		} else {
			result.Status = "ok"
		}
	}
	if result.LocalBindStatus != "ok" {
		result.Status = "failed"
	}
	if flags.NoICMP {
		result.ICMPStatus = "skipped"
		return result
	}
	host := alias.HostName
	if host == "" {
		host = alias.Name
	}
	icmp := runICMPCheckProbe(host, flags.ICMPTimeout)
	result.ICMPStatus = icmp.Status
	result.ICMPRttMillis = icmp.Duration.Milliseconds()
	return result
}

func buildCheckProbe(base sshcmd.Command, alias string, hops []string, timeout time.Duration) sshcmd.Command {
	argv := append([]string(nil), base.Argv...)
	argv = append(argv, "-o", "BatchMode=yes", "-o", fmt.Sprintf("ConnectTimeout=%d", max(1, int(timeout.Seconds()))))
	if len(hops) > 0 {
		argv = append(argv, "-J", strings.Join(hops, ","))
	}
	argv = append(argv, alias, "true")
	return sshcmd.Command{Argv: argv}
}

func defaultRunSSHCheckProbe(cmd sshcmd.Command, timeout time.Duration) sshProbeResult {
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if len(cmd.Argv) == 0 {
		return sshProbeResult{Err: errors.New("empty SSH command"), ExitCode: 1}
	}
	if err := sshcmd.ValidateCommandBinary(cmd, sshcmd.BinaryRequirement{Name: "ssh", Role: "SSH client", Hint: sshcmd.OpenSSHClientInstallHint}); err != nil {
		return sshProbeResult{Duration: time.Since(started), ExitCode: 1, Err: err}
	}
	proc := exec.CommandContext(ctx, cmd.Argv[0], cmd.Argv[1:]...)
	err := proc.Run()
	duration := time.Since(started)
	if ctx.Err() == context.DeadlineExceeded {
		return sshProbeResult{Duration: duration, ExitCode: 124, Err: ctx.Err()}
	}
	if err == nil {
		return sshProbeResult{Duration: duration, ExitCode: 0}
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return sshProbeResult{Duration: duration, ExitCode: exitErr.ExitCode(), Err: err}
	}
	return sshProbeResult{Duration: duration, ExitCode: 1, Err: err}
}

func defaultRunICMPCheckProbe(host string, timeout time.Duration) icmpProbeResult {
	host = strings.TrimSpace(host)
	if host == "" {
		return icmpProbeResult{Status: "unavailable"}
	}
	if _, err := exec.LookPath("ping"); err != nil {
		return icmpProbeResult{Status: "unavailable"}
	}
	args := []string{"-c", "1"}
	switch runtime.GOOS {
	case "darwin":
		args = append(args, "-W", strconv.Itoa(max(1, int(timeout.Milliseconds()))))
	default:
		args = append(args, "-W", strconv.Itoa(max(1, int(timeout.Seconds()))))
	}
	args = append(args, host)
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), timeout+time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ping", args...).CombinedOutput()
	duration := time.Since(started)
	if err != nil {
		return icmpProbeResult{Status: "failed"}
	}
	if parsed, ok := parsePingRTT(string(out)); ok {
		duration = parsed
	}
	return icmpProbeResult{Duration: duration, Status: "ok"}
}

func parsePingRTT(output string) (time.Duration, bool) {
	for _, marker := range []string{"time=", "time<"} {
		idx := strings.Index(output, marker)
		if idx < 0 {
			continue
		}
		rest := output[idx+len(marker):]
		end := 0
		for end < len(rest) && ((rest[end] >= '0' && rest[end] <= '9') || rest[end] == '.') {
			end++
		}
		if end == 0 {
			continue
		}
		ms, err := strconv.ParseFloat(rest[:end], 64)
		if err == nil {
			return time.Duration(ms * float64(time.Millisecond)), true
		}
	}
	return 0, false
}

func defaultLocalBindCheck(bind string, port int) string {
	if bind == "" {
		bind = sshcmd.DefaultForwardBind
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(bind, strconv.Itoa(port)))
	if err != nil {
		return "in_use"
	}
	_ = ln.Close()
	return "ok"
}

func checkErrString(err error, exitCode int) string {
	if err != nil {
		return err.Error()
	}
	if exitCode != 0 {
		return fmt.Sprintf("ssh exited %d", exitCode)
	}
	return ""
}

func printCheckTable(stdout io.Writer, results []checkResult) {
	if len(results) == 0 {
		fmt.Fprintln(stdout, "No checks selected.")
		return
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "KIND\tNAME\tSTATUS\tSSH_MS\tICMP\tICMP_MS\tLOCAL_BIND\tMESSAGE")
	for _, r := range results {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%d\t%s\t%s\n",
			r.Kind, r.Name, r.Status, r.SSHRttMillis, r.ICMPStatus, r.ICMPRttMillis, r.LocalBindStatus, r.Message)
	}
	_ = tw.Flush()
}
