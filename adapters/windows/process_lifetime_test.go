package windows

import (
	"errors"
	"reflect"
	"testing"
)

type fakeSuspendedChild struct {
	fail    string
	steps   []string
	running bool
}

func (f *fakeSuspendedChild) step(name string) error {
	f.steps = append(f.steps, name)
	if f.fail == name {
		return errors.New(name + " failed")
	}
	return nil
}
func (f *fakeSuspendedChild) Prepare() error        { return f.step("prepare") }
func (f *fakeSuspendedChild) StartSuspended() error { return f.step("start suspended") }
func (f *fakeSuspendedChild) Assign() error         { return f.step("assign") }
func (f *fakeSuspendedChild) Resume() error {
	if err := f.step("resume"); err != nil {
		return err
	}
	f.running = true
	return nil
}
func (f *fakeSuspendedChild) Kill() error  { f.running = false; return f.step("kill") }
func (f *fakeSuspendedChild) Wait() error  { return f.step("wait") }
func (f *fakeSuspendedChild) Close() error { f.running = false; return f.step("close") }

func TestOwnedEngineCannotRunBeforeJobAssignment(t *testing.T) {
	f := &fakeSuspendedChild{}
	close, err := startWithLifetime(f)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.steps, []string{"prepare", "start suspended", "assign", "resume"}) || !f.running {
		t.Fatalf("unsafe startup order: %v", f.steps)
	}
	if err := close(); err != nil {
		t.Fatal(err)
	}
	if f.running {
		t.Fatal("closing the owner did not stop its child")
	}
}

func TestOwnedEngineAssignmentFailureNeverResumes(t *testing.T) {
	f := &fakeSuspendedChild{fail: "assign"}
	if close, err := startWithLifetime(f); err == nil || close != nil {
		t.Fatal("unowned engine start accepted")
	}
	if !reflect.DeepEqual(f.steps, []string{"prepare", "start suspended", "assign", "kill", "close", "wait"}) || f.running {
		t.Fatalf("failed assignment escaped cleanup: %v", f.steps)
	}
}

func TestOwnedEngineResumeFailureKillsAndReaps(t *testing.T) {
	f := &fakeSuspendedChild{fail: "resume"}
	if _, err := startWithLifetime(f); err == nil {
		t.Fatal("resume failure accepted")
	}
	if !reflect.DeepEqual(f.steps, []string{"prepare", "start suspended", "assign", "resume", "kill", "close", "wait"}) {
		t.Fatalf("failed resume escaped cleanup: %v", f.steps)
	}
}

func TestOwnedEnginePreStartFailureDoesNotKillOtherProcesses(t *testing.T) {
	for _, stage := range []string{"prepare", "start suspended"} {
		f := &fakeSuspendedChild{fail: stage}
		if _, err := startWithLifetime(f); err == nil {
			t.Fatal("startup failure accepted")
		}
		for _, step := range f.steps {
			if step == "kill" || step == "wait" || step == "resume" {
				t.Fatalf("nonexistent child operated on: %v", f.steps)
			}
		}
		if f.steps[len(f.steps)-1] != "close" {
			t.Fatal("job handle leaked")
		}
	}
}
