package sessionview

import (
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/0xbenc/ssherpa/internal/state"
	"github.com/0xbenc/ssherpa/internal/termstyle"
)

func TestMapViewCarriesRouteDetails(t *testing.T) {
	record := state.SessionRecord{
		ID:               "child",
		Depth:            2,
		TargetAlias:      "prod",
		Route:            []string{"laptop", "bastion", "prod"},
		Hops:             []string{"bastion"},
		StartedAt:        time.Unix(1700000000, 0),
		LocalPID:         os.Getpid(),
		DisconnectReason: "latency_timeout",
	}

	view := MapView(ViewOptions{
		Title:    "ssherpa session map",
		StateDir: "/tmp/ssherpa-state",
		Records:  []state.SessionRecord{record},
		Map:      MapOptions{CurrentID: "child"},
		Theme:    termstyle.TerminalTheme().WithNoColor(true),
		Width:    96,
		Height:   20,
		Help:     "q close",
	})

	text := view.Content
	for _, want := range []string{
		"ssherpa session map",
		"active 1",
		"state /tmp/ssherpa-state",
		"● current  prod [jump]",
		"depth 2  id child",
		"⌂ here local",
		"· laptop hop",
		"· bastion hop",
		"● prod target",
		"health disconnected: latency_timeout",
		"q close",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("view missing %q:\n%s", want, text)
		}
	}
	if strings.Index(text, "· laptop hop") >= strings.Index(text, "· bastion hop") || strings.Index(text, "· bastion hop") >= strings.Index(text, "● prod target") {
		t.Fatalf("route nodes are not ordered as a chain:\n%s", text)
	}
}

func TestMapViewSynthesizesInheritedLineage(t *testing.T) {
	record := state.SessionRecord{
		ID:          "c-session",
		ParentID:    "missing-b-session",
		Depth:       2,
		OriginHost:  "A",
		TargetAlias: "C",
		Route:       []string{"B", "C"},
		StartedAt:   time.Unix(1700000000, 0),
		LocalPID:    os.Getpid(),
	}

	view := MapView(ViewOptions{
		Title:    "ssherpa session map",
		StateDir: "/tmp/ssherpa-state",
		Records:  []state.SessionRecord{record},
		Map:      MapOptions{CurrentID: "c-session"},
		Theme:    termstyle.TerminalTheme().WithNoColor(true),
		Width:    96,
		Height:   20,
	})

	text := view.Content
	for _, want := range []string{
		"active 1",
		"shown 1",
		"recorded 1",
		"● current  C [jump]",
		"depth 2  id c-session",
		"⌂ A local",
		"◆ B ssherpa",
		"● C target",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("view missing %q:\n%s", want, text)
		}
	}
	if strings.Index(text, "⌂ A local") >= strings.Index(text, "◆ B ssherpa") ||
		strings.Index(text, "◆ B ssherpa") >= strings.Index(text, "● C target") {
		t.Fatalf("inherited lineage is not ordered as a chain:\n%s", text)
	}
}

func TestMapLinesSynthesizesInheritedLineage(t *testing.T) {
	record := state.SessionRecord{
		ID:          "c-session",
		ParentID:    "missing-b-session",
		Depth:       2,
		OriginHost:  "A",
		TargetAlias: "C",
		Route:       []string{"B", "C"},
		StartedAt:   time.Unix(1700000000, 0),
		LocalPID:    os.Getpid(),
	}

	text := strings.Join(MapLines("/tmp/ssherpa-state", []state.SessionRecord{record}, "c-session"), "\n")
	for _, want := range []string{
		"active: 1",
		"● current  C [jump]",
		"depth 2  id c-session",
		"⌂ A local",
		"◆ B ssherpa",
		"● C target",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("map lines missing %q:\n%s", want, text)
		}
	}
}

func TestMapMarksRemoteSSHERPABoundaryDistinctFromPlainHops(t *testing.T) {
	root := state.SessionRecord{
		ID:          "root-session",
		OriginHost:  "A",
		TargetAlias: "B",
		Route:       []string{"B"},
		StartedAt:   time.Unix(1700000000, 0),
		LocalPID:    os.Getpid(),
		RunnerMode:  "supervised",
	}
	child := state.SessionRecord{
		ID:           "child-session",
		ParentID:     "root-session",
		Depth:        1,
		OriginHost:   "A",
		TargetAlias:  "E",
		Route:        []string{"B", "C", "D", "E"},
		Hops:         []string{"C", "D"},
		StartedAt:    time.Unix(1700000060, 0),
		RunnerMode:   "supervised",
		RemoteMirror: true,
	}
	records := []state.SessionRecord{root, child}

	active, exited := CountStatuses(records)
	if active != 2 || exited != 0 {
		t.Fatalf("CountStatuses = active %d exited %d, want local and remote supervised sessions active", active, exited)
	}
	if got := StatusLabel(child); got != "remote" {
		t.Fatalf("StatusLabel(remote mirror) = %q, want remote", got)
	}

	text := strings.Join(MapLines("/tmp/ssherpa-state", records, "root-session"), "\n")
	for _, want := range []string{
		"● current  B",
		"◆ ssherpa  E [jump]",
		"◆ B ssherpa",
		"· C hop",
		"· D hop",
		"● E target",
		"remote supervised",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("map missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "◆ C ssherpa") || strings.Contains(text, "◆ D ssherpa") {
		t.Fatalf("plain SSH hops were marked as ssherpa boundaries:\n%s", text)
	}
}

func TestSessionViewTreatsOpenDeadRecordsAsOrphans(t *testing.T) {
	ended := time.Unix(1700000100, 0)
	records := []state.SessionRecord{
		{
			ID:          "live",
			TargetAlias: "prod",
			Route:       []string{"prod"},
			StartedAt:   time.Unix(1700000000, 0),
			LocalPID:    os.Getpid(),
		},
		{
			ID:          "stale",
			TargetAlias: "old-prod",
			Route:       []string{"old-prod"},
			StartedAt:   time.Unix(1700000001, 0),
			LocalPID:    0,
		},
		{
			ID:          "done",
			TargetAlias: "db",
			Route:       []string{"db"},
			StartedAt:   time.Unix(1700000002, 0),
			EndedAt:     &ended,
		},
	}

	active, exited := CountStatuses(records)
	if active != 1 || exited != 2 {
		t.Fatalf("CountStatuses = active %d exited %d, want live only active and stale inactive", active, exited)
	}
	if got := ActiveRecords(records); len(got) != 1 || got[0].ID != "live" {
		t.Fatalf("ActiveRecords = %#v, want only live", got)
	}
	if got := StatusLabel(records[1]); got != "orphan" {
		t.Fatalf("StatusLabel(orphan) = %q, want orphan", got)
	}

	activeText := strings.Join(MapLines("/tmp/ssherpa-state", records, ""), "\n")
	assertContainsText(t, activeText, "active: 1")
	assertContainsText(t, activeText, "● active  prod")
	assertNotContainsText(t, activeText, "old-prod [orphan]")

	allText := strings.Join(MapLinesWithOptions("/tmp/ssherpa-state", records, MapOptions{IncludeExited: true}), "\n")
	assertContainsText(t, allText, "active: 1  exited: 2  total: 3")
	assertContainsText(t, allText, "◌ orphan  old-prod")
}

func TestMapForestMarksInheritedLineage(t *testing.T) {
	record := state.SessionRecord{
		ID:          "c-session",
		ParentID:    "missing-b-session",
		Depth:       2,
		OriginHost:  "A",
		TargetAlias: "C",
		Route:       []string{"B", "C"},
		StartedAt:   time.Unix(1700000000, 0),
	}

	roots := MapForest([]state.SessionRecord{record})
	if len(roots) != 1 || !roots[0].Record.Inherited || roots[0].Record.TargetAlias != "A" {
		t.Fatalf("roots = %#v, want inherited A root", roots)
	}
	if len(roots[0].Children) != 1 || !roots[0].Children[0].Record.Inherited || roots[0].Children[0].Record.TargetAlias != "B" {
		t.Fatalf("children = %#v, want inherited B child", roots[0].Children)
	}
	if len(roots[0].Children[0].Children) != 1 || roots[0].Children[0].Children[0].Record.ID != "c-session" {
		t.Fatalf("grandchildren = %#v, want real C record", roots[0].Children[0].Children)
	}
}

func TestMapViewShowsLocalOriginForSingleHopRoute(t *testing.T) {
	record := state.SessionRecord{
		ID:          "root",
		Depth:       0,
		TargetAlias: "prod",
		Route:       []string{"prod"},
		StartedAt:   time.Unix(1700000000, 0),
		LocalPID:    os.Getpid(),
	}

	view := MapView(ViewOptions{
		Title:    "ssherpa session map",
		StateDir: "/tmp/ssherpa-state",
		Records:  []state.SessionRecord{record},
		Theme:    termstyle.TerminalTheme().WithNoColor(true),
		Width:    96,
		Height:   20,
	})

	if !strings.Contains(view.Content, "⌂ here local") || !strings.Contains(view.Content, "● prod target") {
		t.Fatalf("view missing local origin:\n%s", view.Content)
	}
}

func TestMapViewDoesNotRenderSeparateHopsRow(t *testing.T) {
	record := state.SessionRecord{
		ID:          "root",
		Depth:       0,
		TargetAlias: "prod",
		Kind:        state.KindTunnel,
		Route:       []string{"bastion", "prod"},
		Hops:        []string{"bastion"},
		StartedAt:   time.Unix(1700000000, 0),
		LocalPID:    os.Getpid(),
		Forward: &state.ForwardSpec{
			LocalBind:  "127.0.0.1",
			LocalPort:  15432,
			RemoteHost: "127.0.0.1",
			RemotePort: 5432,
		},
	}

	view := MapView(ViewOptions{
		Title:    "ssherpa session map",
		StateDir: "/tmp/ssherpa-state",
		Records:  []state.SessionRecord{record},
		Theme:    termstyle.TerminalTheme().WithNoColor(true),
		Width:    96,
		Height:   20,
	})

	if strings.Contains(view.Content, "hops") {
		t.Fatalf("view rendered separate hops row:\n%s", view.Content)
	}
	for _, want := range []string{
		"● active  prod [forward]",
		"forward local :15432 -> remote :5432",
	} {
		if !strings.Contains(view.Content, want) {
			t.Fatalf("view missing %q:\n%s", want, view.Content)
		}
	}
}

func TestForwardSummaryIncludesEndpointDetails(t *testing.T) {
	record := state.SessionRecord{
		Kind: state.KindTunnel,
		Forward: &state.ForwardSpec{
			LocalBind:  "0.0.0.0",
			LocalPort:  15432,
			RemoteHost: "db.internal",
			RemotePort: 5432,
			SavedAlias: "pg-tunnel",
			Detached:   true,
			RetryCount: 2,
		},
	}

	want := "local 0.0.0.0:15432 -> remote db.internal:5432  (saved pg-tunnel, background, retries 2)"
	if got := ForwardSummary(record); got != want {
		t.Fatalf("ForwardSummary = %q, want %q", got, want)
	}
}

func TestForwardSummaryOmitsLoopbackHost(t *testing.T) {
	record := state.SessionRecord{
		Kind: state.KindTunnel,
		Forward: &state.ForwardSpec{
			LocalBind:  "127.0.0.1",
			LocalPort:  15432,
			RemoteHost: "127.0.0.1",
			RemotePort: 5432,
		},
	}

	if got, want := ForwardSummary(record), "local :15432 -> remote :5432"; got != want {
		t.Fatalf("ForwardSummary = %q, want %q", got, want)
	}
}

func TestMapViewShowsProxyDetails(t *testing.T) {
	record := state.SessionRecord{
		ID:          "proxy",
		Kind:        state.KindProxy,
		TargetAlias: "bastion",
		Route:       []string{"bastion"},
		StartedAt:   time.Unix(1700000000, 0),
		LocalPID:    os.Getpid(),
		Proxy: &state.ProxySpec{
			Bind:       "127.0.0.1",
			Port:       1080,
			SavedAlias: "corp-proxy",
			Detached:   true,
		},
	}
	view := MapView(ViewOptions{
		Title:    "ssherpa session map",
		StateDir: "/tmp/ssherpa-state",
		Records:  []state.SessionRecord{record},
		Theme:    termstyle.TerminalTheme().WithNoColor(true),
		Width:    96,
		Height:   20,
	})
	for _, want := range []string{
		"● active  bastion [proxy]",
		"proxy SOCKS :1080  (saved corp-proxy, background)",
	} {
		if !strings.Contains(view.Content, want) {
			t.Fatalf("view missing %q:\n%s", want, view.Content)
		}
	}
}

func TestMapViewShowsRemoteCWDAndPrompt(t *testing.T) {
	record := state.SessionRecord{
		ID:           "remote-state",
		TargetAlias:  "prod",
		Route:        []string{"prod"},
		StartedAt:    time.Unix(1700000000, 0),
		LocalPID:     os.Getpid(),
		RemoteHost:   "prod.example.com",
		RemoteCWD:    "/srv/app",
		RemotePrompt: state.RemotePromptPrompt,
	}
	view := MapView(ViewOptions{
		Title:    "ssherpa session map",
		StateDir: "/tmp/ssherpa-state",
		Records:  []state.SessionRecord{record},
		Theme:    termstyle.TerminalTheme().WithNoColor(true),
		Width:    96,
		Height:   20,
	})
	for _, want := range []string{
		"remote cwd prod.example.com:/srv/app  prompt idle",
	} {
		if !strings.Contains(view.Content, want) {
			t.Fatalf("view missing %q:\n%s", want, view.Content)
		}
	}
}

func TestRemoteSummaryShowsRunningPrompt(t *testing.T) {
	record := state.SessionRecord{RemotePrompt: state.RemotePromptRunning}
	if got, want := RemoteSummary(record), "prompt running"; got != want {
		t.Fatalf("RemoteSummary = %q, want %q", got, want)
	}
}

func TestFormatDisplayRouteKeepsInheritedOrigin(t *testing.T) {
	if got := FormatDisplayRoute([]string{"laptop", "bastion", "prod"}); got != "here -> laptop -> bastion -> prod" {
		t.Fatalf("FormatDisplayRoute = %q", got)
	}
	if got := FormatDisplayRoute([]string{"prod"}); got != "here -> prod" {
		t.Fatalf("FormatDisplayRoute = %q", got)
	}
	if got := FormatDisplayRoute([]string{"here", "prod"}); got != "here -> prod" {
		t.Fatalf("FormatDisplayRoute = %q", got)
	}
}

func TestMapModelWaitsForKey(t *testing.T) {
	m := mapModel{
		noAltScreen: true,
		view: ViewOptions{
			Title:    "ssherpa session map",
			StateDir: "/tmp/ssherpa-state",
			Theme:    termstyle.TerminalTheme().WithNoColor(true),
		},
		width:  80,
		height: 20,
	}

	view := m.View()
	if view.AltScreen {
		t.Fatalf("AltScreen = true, want false")
	}
	if !strings.Contains(view.Content, "press any key to return") {
		t.Fatalf("view missing return hint:\n%s", view.Content)
	}
	_, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: 'q', Text: "q"}))
	if cmd == nil {
		t.Fatalf("key press did not request quit")
	}
}

func assertContainsText(t *testing.T, got string, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("text missing %q:\n%s", want, got)
	}
}

func assertNotContainsText(t *testing.T, got string, want string) {
	t.Helper()
	if strings.Contains(got, want) {
		t.Fatalf("text unexpectedly contains %q:\n%s", want, got)
	}
}
