//go:build windows

package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Execute cannot be started in a unit test: it owns a real service and daemon.
// Guard its startup boundary without launching either on the developer machine.
func TestServicePublishesQueryableProcessBeforeDaemon(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "service_windows.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var execute *ast.FuncDecl
	for _, declaration := range file.Decls {
		if fn, ok := declaration.(*ast.FuncDecl); ok && fn.Name.Name == "Execute" && fn.Recv != nil {
			execute = fn
		}
	}
	if execute == nil {
		t.Fatal("service Execute method missing")
	}
	guard, daemon := token.NoPos, token.NoPos
	ast.Inspect(execute, func(node ast.Node) bool {
		if call, ok := node.(*ast.CallExpr); ok {
			if name, ok := call.Fun.(*ast.Ident); ok {
				switch name.Name {
				case "enableServiceProcessQuery":
					guard = call.Pos()
				case "buildDaemon":
					daemon = call.Pos()
				}
			}
		}
		return true
	})
	if guard == token.NoPos || daemon == token.NoPos || guard >= daemon {
		t.Fatal("service must grant and verify its metadata query ACL before daemon construction/listening/Running")
	}
	// A grant/readback failure must return a failed service start, rather than
	// merely logging and continuing toward the listener or Running state.
	guardedFailure := false
	for _, statement := range execute.Body.List {
		branch, ok := statement.(*ast.IfStmt)
		if !ok || branch.Init == nil {
			continue
		}
		assignment, ok := branch.Init.(*ast.AssignStmt)
		if !ok || len(assignment.Rhs) != 1 {
			continue
		}
		call, ok := assignment.Rhs[0].(*ast.CallExpr)
		if !ok {
			continue
		}
		name, ok := call.Fun.(*ast.Ident)
		if !ok || name.Name != "enableServiceProcessQuery" {
			continue
		}
		condition, ok := branch.Cond.(*ast.BinaryExpr)
		if !ok || condition.Op != token.NEQ {
			t.Fatal("process-query startup error is not checked")
		}
		left, leftOK := condition.X.(*ast.Ident)
		right, rightOK := condition.Y.(*ast.Ident)
		if !leftOK || !rightOK || left.Name != "err" || right.Name != "nil" {
			t.Fatal("process-query startup error condition changed")
		}
		last, ok := branch.Body.List[len(branch.Body.List)-1].(*ast.ReturnStmt)
		if ok && len(last.Results) == 2 {
			failure, ok := last.Results[1].(*ast.BasicLit)
			guardedFailure = ok && failure.Kind == token.INT && failure.Value == "1"
		}
	}
	if !guardedFailure {
		t.Fatal("process-query ACL failure can continue service startup")
	}
}

// These tests use only in-memory security descriptors and the Windows ACL
// parser/merger. They never open a service, process, pipe, or network interface.
func TestServiceProcessQueryACLAddsOnlyInteractiveMetadata(t *testing.T) {
	original := processDescriptor(t, "O:SYG:SYD:P(A;;GA;;;SY)(A;;GA;;;BA)")
	before := original.String()
	merged, err := serviceProcessQueryACL(original)
	if err != nil {
		t.Fatal(err)
	}
	text, err := processACLString(merged)
	if err != nil {
		t.Fatal(err)
	}
	if original.String() != before {
		t.Fatal("merging changed the original descriptor")
	}
	for _, retained := range []string{"(A;;GA;;;SY)", "(A;;GA;;;BA)"} {
		if !strings.Contains(text, retained) {
			t.Fatalf("lost original ACE %s: %s", retained, text)
		}
	}
	if merged.AceCount != 3 {
		t.Fatalf("expected exactly the two original ACEs and interactive query grant: %s", text)
	}
	iu, err := windows.CreateWellKnownSid(windows.WinInteractiveSid)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for i := uint32(0); i < uint32(merged.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(merged, i, &ace); err != nil {
			t.Fatal(err)
		}
		if ace.Header.AceType == windows.ACCESS_ALLOWED_ACE_TYPE && windows.EqualSid((*windows.SID)(unsafe.Pointer(&ace.SidStart)), iu) {
			found = true
			if ace.Mask != windows.PROCESS_QUERY_LIMITED_INFORMATION || ace.Header.AceFlags != 0 {
				t.Fatalf("interactive grant widened or became inheritable: mask=%#x flags=%#x", ace.Mask, ace.Header.AceFlags)
			}
		}
	}
	if !found {
		t.Fatal("interactive metadata query ACE missing")
	}
	// Re-applying on a descriptor that already carries the grant adds nothing.
	second, err := serviceProcessQueryACL(processDescriptor(t, text))
	if err != nil {
		t.Fatal(err)
	}
	again, err := processACLString(second)
	if err != nil || again != text {
		t.Fatalf("grant is not idempotent: first=%s again=%s err=%v", text, again, err)
	}
}

func TestServiceProcessQueryACLPreservesExistingDenialsAndGrants(t *testing.T) {
	// Existing denials stay authoritative, including a denial of query itself.
	// The grant never revokes them or rewrites a machine's stricter policy.
	for _, sddl := range []string{
		"D:P(D;;0x1;;;IU)(A;;GA;;;SY)(A;;GA;;;BA)(A;;0x20000;;;LS)",
		"D:P(D;;0x1000;;;WD)(A;;GA;;;SY)(A;;GA;;;BA)",
	} {
		original := processDescriptor(t, sddl)
		merged, err := serviceProcessQueryACL(original)
		if err != nil {
			t.Fatal(err)
		}
		text, err := processACLString(merged)
		if err != nil {
			t.Fatal(err)
		}
		originalACL, _, _ := original.DACL()
		if merged.AceCount != originalACL.AceCount+1 {
			t.Fatalf("existing entries lost or unexpected entries added: %s", text)
		}
		for _, ace := range strings.Split(original.String(), "(")[1:] {
			if !strings.Contains(text, "("+ace) {
				t.Fatalf("existing ACE changed: (%s in %s", ace, text)
			}
		}
	}
}

func TestServiceProcessQueryACLRejectsUnrestrictedOrInvalidDescriptor(t *testing.T) {
	for name, original := range map[string]*windows.SECURITY_DESCRIPTOR{
		"nil":     nil,
		"invalid": new(windows.SECURITY_DESCRIPTOR),
		"absent":  processDescriptor(t, "O:SY"),
		"null":    processDescriptor(t, "D:NO_ACCESS_CONTROL"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := serviceProcessQueryACL(original); err == nil {
				t.Fatal("accepted an invalid or fully permissive process DACL")
			}
		})
	}
}

func TestServiceProcessQueryACLReadbackRejectsMissingOrWidenedEntries(t *testing.T) {
	original := processDescriptor(t, "D:P(D;;0x1;;;IU)(A;;GA;;;SY)(A;;GA;;;BA)")
	expected, err := serviceProcessQueryACL(original)
	if err != nil {
		t.Fatal(err)
	}
	text, err := processACLString(expected)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyServiceProcessQueryACL(expected, processDescriptor(t, text)); err != nil {
		t.Fatal(err)
	}
	for name, actual := range map[string]*windows.SECURITY_DESCRIPTOR{
		"nil":           nil,
		"invalid":       new(windows.SECURITY_DESCRIPTOR),
		"null":          processDescriptor(t, "D:NO_ACCESS_CONTROL"),
		"missing-grant": original,
		"lost-denial":   processDescriptor(t, "D:(A;;GA;;;SY)(A;;GA;;;BA)(A;;0x1000;;;IU)"),
		"lost-admin":    processDescriptor(t, "D:(D;;0x1;;;IU)(A;;GA;;;SY)(A;;0x1000;;;IU)"),
		"broad-iu":      processDescriptor(t, "D:(D;;0x1;;;IU)(A;;GA;;;SY)(A;;GA;;;BA)(A;;GA;;;IU)"),
	} {
		t.Run(name, func(t *testing.T) {
			if err := verifyServiceProcessQueryACL(expected, actual); err == nil {
				t.Fatal("unverified process ACL accepted as ready")
			}
		})
	}
}

func processDescriptor(t *testing.T, sddl string) *windows.SECURITY_DESCRIPTOR {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	return sd
}
