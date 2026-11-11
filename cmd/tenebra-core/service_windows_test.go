//go:build windows

package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"
)

// TestFindBundledSingbox walks the two layouts the service resolves against:
// the Tauri bundle (resources\ next to the executable), the flat layout
// (sing-box beside the executable), and the bundle winning when both exist.
func TestFindBundledSingbox(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		if got := findBundledSingbox(t.TempDir()); got != "" {
			t.Errorf("findBundledSingbox on empty dir = %q, want empty", got)
		}
	})

	t.Run("flat", func(t *testing.T) {
		dir := t.TempDir()
		want := filepath.Join(dir, "sing-box.exe")
		if err := os.WriteFile(want, []byte("fake"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := findBundledSingbox(dir); got != want {
			t.Errorf("findBundledSingbox flat = %q, want %q", got, want)
		}
	})

	t.Run("bundle wins", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "resources"), 0o755); err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(dir, "resources", "sing-box.exe")
		for _, p := range []string{want, filepath.Join(dir, "sing-box.exe")} {
			if err := os.WriteFile(p, []byte("fake"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if got := findBundledSingbox(dir); got != want {
			t.Errorf("findBundledSingbox bundle = %q, want %q", got, want)
		}
	})
}

// TestServiceStartsTheDaemonsBackgroundJobs is a source-level check, and it is
// deliberate: the thing it guards cannot be reached from a test any other way.
//
// coreService.Execute is called by the service control manager, needs a real SCM
// handshake and a real named pipe, and blocks until the machine shuts down — so
// no unit test drives it. The absence it exists to catch had exactly that shape:
// main() hands off to maybeRunService before it ever reaches run(), so the
// background jobs launched in run() never ran under a service install, which is
// every ordinary Windows install. The DPI bypass never installed itself and never
// updated; it arrived only as a side effect of connecting, and only until the
// user's next reinstall. Nothing failed, nothing was logged, and the release
// looked healthy from every angle a test could see.
//
// So the invariant is stated where it can be: whatever else Execute does, it hands
// the daemon's own background work a context, the same way the console path does.
func TestServiceStartsTheDaemonsBackgroundJobs(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "service_windows.go", nil, 0)
	if err != nil {
		t.Fatalf("parse service_windows.go: %v", err)
	}

	var execute *ast.FuncDecl
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if ok && fn.Name.Name == "Execute" && fn.Recv != nil {
			execute = fn
		}
		return execute == nil
	})
	if execute == nil {
		t.Fatal("coreService.Execute not found; this test needs rewriting, not deleting")
	}

	found := false
	ast.Inspect(execute, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "startBackgroundJobs" {
			found = true
		}
		return !found
	})
	if !found {
		t.Error("the service path does not start the daemon's background jobs: " +
			"a service install gets no bundle install and no bypass updates at all")
	}
}
