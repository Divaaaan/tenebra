package installtx

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type packageInstallFS struct {
	fakeInstallFS
	helper string
}

func (f packageInstallFS) Stage(ctx context.Context, source Source, m Manifest) (StagedPayload, error) {
	p, err := f.fakeInstallFS.Stage(ctx, source, m)
	p.Runtime.Provenance = "package_manager"
	p.Runtime.HelperSHA256 = f.helper
	return p, err
}
func (f packageInstallFS) HelperIdentity() string { return f.helper }
func packageStageFixture(manager string, first bool) (*installDisk, *Installer, ApplyRequest) {
	d := installerDisk()
	d.journal.Manager = manager
	if first {
		d.journal.Active = nil
		d.journal.Transaction = nil
		d.running = nil
		d.admitted = false
	}
	request := installRequest(d)
	request.Trust = "package_manager"
	request.RestartAck = ""
	request.Source = &Source{Layout: "installed_package", AbsolutePath: "/usr/lib/tenebra/package-payload"}
	files := packageInstallFS{fakeInstallFS: fakeInstallFS{d}, helper: strings.Repeat("c", 64)}
	return d, New(files, fakeInstallStore{d}, fakeInstallService{d}, fakeInstallProbe{d}), request
}
func packageLifecycleCalls(calls []string) int {
	n := 0
	for _, call := range calls {
		if call == "stop" || call == "fence" || strings.HasPrefix(call, "start:") || strings.HasPrefix(call, "checkpoint:") || strings.HasPrefix(call, "service-admit:") {
			n++
		}
	}
	return n
}
func TestPackageFirstConfigureAndPacmanStageHaveDifferentActivation(t *testing.T) {
	for _, manager := range []string{"dpkg", "pacman"} {
		d, i, r := packageStageFixture(manager, true)
		result, err := i.StagePackage(context.Background(), r, manager)
		if err != nil {
			t.Fatal(manager, err, d.calls)
		}
		if manager == "dpkg" && (result.Status.Phase != "committed_open" || d.journal.Active == nil) {
			t.Fatal("first dpkg configure did not activate idle service", result, d.calls)
		}
		if manager == "pacman" && (result.Status.Phase != "pending" || d.journal.Active != nil || packageLifecycleCalls(d.calls) != 0) {
			t.Fatal("first pacman stage activated without explicit enable", result, d.calls)
		}
	}
}
func TestPackageUpgradeAndRepeatedPostinstNeverRestartActiveRuntime(t *testing.T) {
	for _, manager := range []string{"dpkg", "pacman"} {
		d, i, r := packageStageFixture(manager, false)
		old := *d.journal.Active
		if result, err := i.StagePackage(context.Background(), r, manager); err != nil || result.Status.Phase != "pending" {
			t.Fatal(result, err)
		}
		if packageLifecycleCalls(d.calls) != 0 || *d.journal.Active != old {
			t.Fatal("package upgrade restarted or moved active runtime", d.calls)
		}
		generation := d.journal.Generation
		d.calls = nil
		if result, err := i.StagePackage(context.Background(), r, manager); err != nil || result.Changed || d.journal.Generation != generation || len(d.calls) != 0 {
			t.Fatal("repeated postinst was not idempotent", result, err, d.calls)
		}
	}
}
func TestPackagePendingHelperRefreshPreservesOldActiveAndNewPayload(t *testing.T) {
	d, i, r := packageStageFixture("dpkg", false)
	old := *d.journal.Active
	if _, err := i.StagePackage(context.Background(), r, "dpkg"); err != nil {
		t.Fatal(err)
	}
	pending := *d.journal.Pending
	generation := d.journal.Generation
	d.calls = nil
	newHelper := strings.Repeat("d", 64)
	files := packageInstallFS{fakeInstallFS: fakeInstallFS{d}, helper: newHelper}
	i = New(files, fakeInstallStore{d}, fakeInstallService{d}, fakeInstallProbe{d})
	if result, err := i.StagePackage(context.Background(), r, "dpkg"); err != nil || !result.Changed {
		t.Fatal(result, err)
	}
	if *d.journal.Active != old || d.journal.Pending.ArtifactSHA256 != pending.ArtifactSHA256 || d.journal.Pending.HelperSHA256 != newHelper || d.journal.Generation != generation+1 || packageLifecycleCalls(d.calls) != 0 {
		t.Fatal("helper-only package refresh changed active authority", d.journal, d.calls)
	}
}
func TestPackageStageRejectsCrossManagerBeforeCopyOrRestart(t *testing.T) {
	d, i, r := packageStageFixture("dpkg", false)
	if _, err := i.StagePackage(context.Background(), r, "pacman"); !errors.Is(err, ErrConflict) || len(d.calls) != 0 {
		t.Fatal("cross-manager staged or stopped service", err, d.calls)
	}
	r.RestartAck = "restart"
	if _, err := i.StagePackage(context.Background(), r, "dpkg"); !errors.Is(err, ErrProtocol) || len(d.calls) != 0 {
		t.Fatal("package hook supplied implicit restart ack", err, d.calls)
	}
}

func TestPackageRepeatedFirstDpkgConfigureIsIdempotent(t *testing.T) {
	d, i, r := packageStageFixture("dpkg", true)
	if _, err := i.StagePackage(context.Background(), r, "dpkg"); err != nil {
		t.Fatal(err)
	}
	generation := d.journal.Generation
	active := *d.journal.Active
	d.calls = nil
	result, err := i.StagePackage(context.Background(), r, "dpkg")
	if err != nil || result.Changed || result.Status.Phase != "committed_open" || d.journal.Generation != generation || *d.journal.Active != active || d.journal.Pending != nil || len(d.calls) != 0 {
		t.Fatal("repeat first configure invented an update or restarted", result, err, d.calls)
	}
}

func TestPackageRemovePendingKeepsProfiles(t *testing.T) {
	for _, tt := range []struct {
		manager string
		first   bool
	}{{manager: "dpkg"}, {manager: "pacman"}, {manager: "pacman", first: true}} {
		t.Run(tt.manager+map[bool]string{false: "_upgrade", true: "_first"}[tt.first], func(t *testing.T) {
			d, installer, request := packageStageFixture(tt.manager, tt.first)
			staged, err := installer.StagePackage(context.Background(), request, tt.manager)
			if err != nil || staged.Status.Phase != "pending" {
				t.Fatal(staged, err)
			}
			profiles := make(map[string]string, len(d.data))
			for id, data := range d.data {
				profiles[id] = data
			}
			before := d.current
			d.calls = nil
			removed, err := installer.Apply(context.Background(), ApplyRequest{
				Schema: 1, OperationID: "so1_" + strings.Repeat("4", 32), Action: "remove", KeepProfiles: true,
			})
			if err != nil || removed.Status.Phase != "removed" || removed.Status.OperationalCommitID == "" {
				t.Fatal("pending package could not be removed", removed, err, d.calls)
			}
			if d.journal.Active != nil || d.journal.Pending != nil || d.running != nil || d.admitted || d.current != before || len(d.data) != len(profiles) {
				t.Fatal("removal changed profiles or kept an active/pending runtime", d.calls)
			}
			for id, data := range profiles {
				if d.data[id] != data {
					t.Fatal("removal changed a profile", id)
				}
			}
			for _, call := range d.calls {
				if strings.HasPrefix(call, "start:") || strings.HasPrefix(call, "checkpoint:") || call == "migrate" || call == "restore" {
					t.Fatal("removal entered pending activation", d.calls)
				}
			}
		})
	}
}
