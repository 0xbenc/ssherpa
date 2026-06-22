package sessionview

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/0xbenc/ssherpa/internal/state"
	"github.com/0xbenc/ssherpa/internal/termstyle"
)

// coldViewOptions builds a stable ViewOptions: a far-past StartedAt keeps the
// relative-time label fixed across the two immediate paints in the determinism
// test.
func coldViewOptions() ViewOptions {
	rec := state.SessionRecord{
		ID:          "child",
		Depth:       2,
		TargetAlias: "prod",
		Route:       []string{"laptop", "bastion", "prod"},
		Hops:        []string{"bastion"},
		StartedAt:   time.Unix(1_500_000_000, 0),
		LocalPID:    os.Getpid(),
	}
	return ViewOptions{
		Title:    "ssherpa session map",
		StateDir: "/tmp/ssherpa-state",
		Records:  []state.SessionRecord{rec},
		Map:      MapOptions{CurrentID: "child"},
		Theme:    termstyle.TerminalTheme().WithNoColor(true),
		Width:    96,
		Height:   20,
		Help:     "q close",
	}
}

// TestMapViewIsDeterministic pins S3: the overlay paint is pure — two paints of
// identical inputs produce byte-identical frames (no clock-tick content, no
// randomness), so identical repaints over the live stream never differ.
func TestMapViewIsDeterministic(t *testing.T) {
	a := MapView(coldViewOptions())
	b := MapView(coldViewOptions())
	if a.Content != b.Content {
		t.Fatalf("MapView is not deterministic:\n--- a ---\n%s\n--- b ---\n%s", a.Content, b.Content)
	}
}

// TestMapViewSchedulesNoGoroutines pins that painting the overlay starts no
// goroutine — the supervised path must stay cold.
func TestMapViewSchedulesNoGoroutines(t *testing.T) {
	runtime.GC()
	before := runtime.NumGoroutine()
	for i := 0; i < 500; i++ {
		_ = MapView(coldViewOptions())
	}
	runtime.GC()
	time.Sleep(10 * time.Millisecond)
	after := runtime.NumGoroutine()
	if after > before {
		t.Fatalf("MapView leaked goroutines: before=%d after=%d", before, after)
	}
}

// TestMapViewBodyHasNoTimers pins structurally that MapView's own body
// schedules no timer and spawns no goroutine — converting the idle-cold
// property from convention to a CI contract.
func TestMapViewBodyHasNoTimers(t *testing.T) {
	assertNoMotion(t, "sessionview.go", "MapView")
}

func assertNoMotion(t *testing.T, file, fn string) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	var body *ast.BlockStmt
	for _, decl := range f.Decls {
		if fd, ok := decl.(*ast.FuncDecl); ok && fd.Name.Name == fn {
			body = fd.Body
		}
	}
	if body == nil {
		t.Fatalf("function %s not found in %s", fn, file)
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.GoStmt:
			t.Errorf("%s spawns a goroutine — forbidden on the overlay paint path", fn)
		case *ast.CallExpr:
			if sel, ok := node.Fun.(*ast.SelectorExpr); ok {
				if pkg, ok := sel.X.(*ast.Ident); ok {
					q := pkg.Name + "." + sel.Sel.Name
					switch q {
					case "time.After", "time.Sleep", "time.Tick", "tea.Tick", "tea.Every":
						t.Errorf("%s calls %s — forbidden on the overlay paint path", fn, q)
					}
				}
			}
		}
		return true
	})
}
