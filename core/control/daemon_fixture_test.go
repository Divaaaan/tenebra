package control

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/Divaaaan/tenebra/core/profile"
)

// A fake engine does not isolate the daemon's other OS adapters. In particular,
// a successful system-proxy connect otherwise runs networksetup on macOS even
// though the fake engine has no listening proxy. Unit fixtures must opt into
// fakes before starting any connection; individual tests may replace them with
// a different scripted implementation when exercising an error path.
func newUnitTestDaemon(store *profile.Store, runner Runner) *Daemon {
	d := NewDaemon(store, runner)
	d.proxy = &fakeProxyController{}
	return d
}

func TestUnitDaemonStartsWithFakeSystemProxy(t *testing.T) {
	store, err := profile.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d := newUnitTestDaemon(store, newFakeRunner())
	t.Cleanup(d.entCancel)
	if _, ok := d.proxy.(*fakeProxyController); !ok {
		t.Fatalf("unit fixture uses host proxy controller %T", d.proxy)
	}
}

// A forgotten injection in a new fixture must fail without needing to mutate
// the test machine to discover it. The only direct production constructor call
// in unit tests belongs to the adapter-injecting factory above.
func TestUnitDaemonConstructorsCannotBypassProxyIsolation(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	calls := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, entry.Name(), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		var factory *ast.FuncDecl
		if entry.Name() == "daemon_fixture_test.go" {
			for _, decl := range file.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "newUnitTestDaemon" {
					factory = fn
				}
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			name, ok := call.Fun.(*ast.Ident)
			if !ok || name.Name != "NewDaemon" {
				return true
			}
			calls++
			if factory == nil || call.Pos() < factory.Pos() || call.End() > factory.End() {
				t.Errorf("%s: use newUnitTestDaemon so the fixture cannot apply the host proxy", fset.Position(call.Pos()))
			}
			return true
		})
	}
	if calls != 1 {
		t.Errorf("direct NewDaemon calls in unit fixtures = %d, want the one isolated factory", calls)
	}
}
