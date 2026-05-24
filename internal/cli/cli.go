package cli

import (
	"fmt"
	"io"
	"strings"
)

const usage = `Usage:
  ssherpa [command]

Available Commands:
  version    Print build version information
  help       Show this help

Phase 0:
  The Go port foundation is present, but SSH alias workflows are not
  implemented yet. The Bash Zoo implementation remains the compatibility
  reference for future phases.
`

type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

func (b BuildInfo) normalized() BuildInfo {
	return BuildInfo{
		Version: defaultString(b.Version, "dev"),
		Commit:  defaultString(b.Commit, "none"),
		Date:    defaultString(b.Date, "unknown"),
	}
}

func Run(args []string, stdout io.Writer, stderr io.Writer, build BuildInfo) int {
	stdout = writerOrDiscard(stdout)
	stderr = writerOrDiscard(stderr)

	if len(args) == 0 {
		printUsage(stdout)
		return 0
	}

	switch args[0] {
	case "version", "--version", "-v":
		if len(args) > 1 {
			fmt.Fprintf(stderr, "ssherpa: version does not accept arguments: %s\n", strings.Join(args[1:], " "))
			return 1
		}
		printVersion(stdout, build)
		return 0
	case "help", "--help", "-h":
		if len(args) > 1 {
			fmt.Fprintf(stderr, "ssherpa: help does not accept arguments: %s\n", strings.Join(args[1:], " "))
			return 1
		}
		printUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "ssherpa: unknown command or flag %q\n\n", args[0])
		printUsage(stderr)
		return 1
	}
}

func printVersion(w io.Writer, build BuildInfo) {
	build = build.normalized()
	fmt.Fprintf(w, "ssherpa %s\n", build.Version)
	fmt.Fprintf(w, "commit: %s\n", build.Commit)
	fmt.Fprintf(w, "built: %s\n", build.Date)
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, usage)
}

func defaultString(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func writerOrDiscard(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}
