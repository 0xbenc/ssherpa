package session

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/0xbenc/ssherpa/internal/termstyle"
)

// TestSessionOverlayLinesDeterministic pins S3: identical inputs paint
// byte-identical frames, so repaints over the live stream never differ.
func TestSessionOverlayLinesDeterministic(t *testing.T) {
	dir := t.TempDir()
	theme := termstyle.TerminalTheme().WithNoColor(true)
	a := sessionOverlayLines(dir, "x", theme, 88, 24, "q close")
	b := sessionOverlayLines(dir, "x", theme, 88, 24, "q close")
	if strings.Join(a, "\n") != strings.Join(b, "\n") {
		t.Fatalf("sessionOverlayLines is not deterministic:\n%q\n%q", a, b)
	}
}

// TestSessionOverlayLinesGoroutineNeutral pins that the overlay paint starts no
// goroutine — the supervised path must stay cold.
func TestSessionOverlayLinesGoroutineNeutral(t *testing.T) {
	dir := t.TempDir()
	theme := termstyle.TerminalTheme().WithNoColor(true)
	runtime.GC()
	before := runtime.NumGoroutine()
	for i := 0; i < 300; i++ {
		_ = sessionOverlayLines(dir, "x", theme, 88, 24, "q close")
	}
	runtime.GC()
	time.Sleep(10 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before {
		t.Fatalf("sessionOverlayLines leaked goroutines: before=%d after=%d", before, after)
	}
}

// TestDrawSessionOverlayNonTerminalSkipsEscapes pins that the piped /
// non-terminal path emits no DECSC/DECRC bottom-strip escapes (only safe for a
// real TTY), so a redirected supervised session can never corrupt.
func TestDrawSessionOverlayNonTerminalSkipsEscapes(t *testing.T) {
	var buf bytes.Buffer
	dir := t.TempDir()
	theme := termstyle.TerminalTheme().WithNoColor(true)
	frame := drawSessionOverlay(&buf, nil, dir, "x", OverlayOptions{}, theme, nil)
	if frame.terminal {
		t.Fatal("nil stdin should take the non-terminal branch")
	}
	if out := buf.String(); strings.Contains(out, "\x1b7") || strings.Contains(out, "\x1b8") {
		t.Fatalf("non-terminal overlay emitted DECSC/DECRC escapes: %q", out)
	}
}

// TestSessionOverlayLinesBodyHasNoTimers pins structurally that the overlay
// paint schedules no timer and spawns no goroutine.
func TestSessionOverlayLinesBodyHasNoTimers(t *testing.T) {
	assertNoMotionSession(t, "session.go", "sessionOverlayLines")
}

func assertNoMotionSession(t *testing.T, file, fn string) {
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
					switch pkg.Name + "." + sel.Sel.Name {
					case "time.After", "time.Sleep", "time.Tick", "tea.Tick", "tea.Every":
						t.Errorf("%s calls %s.%s — forbidden on the overlay paint path", fn, pkg.Name, sel.Sel.Name)
					}
				}
			}
		}
		return true
	})
}
