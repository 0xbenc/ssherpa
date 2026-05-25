package sessionview

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/0xbenc/ssherpa/internal/state"
)

func WriteList(w io.Writer, stateDir string, records []state.SessionRecord) {
	active, exited := CountStatuses(records)
	fmt.Fprintln(w, "Supervised sessions")
	fmt.Fprintf(w, "state: %s\n", stateDir)
	fmt.Fprintf(w, "active: %d  exited: %d  total: %d\n", active, exited, len(records))
	if len(records) == 0 {
		fmt.Fprintln(w, "\nNo supervised sessions recorded.")
		return
	}

	writeListGroup(w, "Active", records, true)
	writeListGroup(w, "Exited", records, false)
}

type MapOptions struct {
	CurrentID     string
	IncludeExited bool
}

func MapLines(stateDir string, records []state.SessionRecord, currentID string) []string {
	return MapLinesWithOptions(stateDir, records, MapOptions{CurrentID: currentID})
}

func MapLinesWithOptions(stateDir string, records []state.SessionRecord, opts MapOptions) []string {
	active, exited := CountStatuses(records)
	visible := records
	if !opts.IncludeExited {
		visible = ActiveRecords(records)
	}

	lines := []string{"Session route map", fmt.Sprintf("state: %s", stateDir)}
	if opts.IncludeExited {
		lines = append(lines, fmt.Sprintf("active: %d  exited: %d  total: %d", active, exited, len(records)))
	} else {
		lines = append(lines, fmt.Sprintf("active: %d", active))
	}
	lines = append(lines, "")

	if len(visible) == 0 {
		if opts.IncludeExited {
			return append(lines, "No supervised sessions recorded.")
		}
		return append(lines, "No active supervised sessions.")
	}

	roots := state.BuildSessionForest(visible)
	for i, root := range roots {
		lines = appendNodeLines(lines, root, "", i == len(roots)-1, opts.CurrentID)
	}
	return lines
}

func WriteMap(w io.Writer, stateDir string, records []state.SessionRecord) {
	WriteMapWithOptions(w, stateDir, records, MapOptions{})
}

func WriteMapWithOptions(w io.Writer, stateDir string, records []state.SessionRecord, opts MapOptions) {
	for _, line := range MapLinesWithOptions(stateDir, records, opts) {
		fmt.Fprintln(w, line)
	}
}

func CountStatuses(records []state.SessionRecord) (active int, exited int) {
	for _, record := range records {
		if record.Status() == "active" {
			active++
		} else {
			exited++
		}
	}
	return active, exited
}

func ActiveRecords(records []state.SessionRecord) []state.SessionRecord {
	active := make([]state.SessionRecord, 0, len(records))
	for _, record := range records {
		if record.Status() == "active" {
			active = append(active, record)
		}
	}
	return active
}

func StatusLabel(record state.SessionRecord) string {
	if record.Status() == "active" {
		return "active"
	}
	if record.ExitCode != nil {
		return fmt.Sprintf("exit %d", *record.ExitCode)
	}
	return "exited"
}

func Target(record state.SessionRecord) string {
	if strings.TrimSpace(record.TargetAlias) != "" {
		return record.TargetAlias
	}
	if len(record.Route) > 0 {
		return record.Route[len(record.Route)-1]
	}
	return "-"
}

func FormatRoute(route []string) string {
	if len(route) == 0 {
		return "-"
	}
	return strings.Join(route, " -> ")
}

func writeListGroup(w io.Writer, title string, records []state.SessionRecord, active bool) {
	first := true
	for _, record := range records {
		if (record.Status() == "active") != active {
			continue
		}
		if first {
			fmt.Fprintf(w, "\n%s\n", title)
			first = false
		}
		fmt.Fprintf(w, "%s\t%s\tdepth=%d\ttarget=%s\troute=%s\tstarted=%s\n",
			record.ID,
			StatusLabel(record),
			record.Depth,
			Target(record),
			FormatRoute(record.Route),
			record.StartedAt.Local().Format(time.RFC3339),
		)
		if health := HealthSummary(record); health != "" {
			fmt.Fprintf(w, "\thealth=%s\n", health)
		}
	}
}

func appendNodeLines(lines []string, node state.SessionNode, prefix string, last bool, currentID string) []string {
	connector := "+- "
	nextPrefix := prefix + "|  "
	if last {
		nextPrefix = prefix + "   "
	}
	record := node.Record
	current := ""
	if currentID != "" && record.ID == currentID {
		current = "  current"
	}
	lines = append(lines, fmt.Sprintf("%s%s%s [%s] depth=%d id=%s%s",
		prefix,
		connector,
		Target(record),
		StatusLabel(record),
		record.Depth,
		record.ID,
		current,
	))
	if len(record.Route) > 0 {
		lines = append(lines, fmt.Sprintf("%s   route: %s", prefix, FormatRoute(record.Route)))
	}
	if len(record.Hops) > 0 {
		lines = append(lines, fmt.Sprintf("%s   hops: %s", prefix, FormatRoute(record.Hops)))
	}
	if health := HealthSummary(record); health != "" {
		lines = append(lines, fmt.Sprintf("%s   health: %s", prefix, health))
	}
	for i, child := range node.Children {
		lines = appendNodeLines(lines, child, nextPrefix, i == len(node.Children)-1, currentID)
	}
	return lines
}

func HealthSummary(record state.SessionRecord) string {
	if record.DisconnectReason != "" {
		return "disconnected: " + record.DisconnectReason
	}
	if len(record.Events) == 0 {
		return ""
	}
	last := record.Events[len(record.Events)-1]
	switch last.Type {
	case "latency_warning":
		return last.Message
	case "latency_disconnect":
		return "disconnected: " + last.Message
	}
	return ""
}
