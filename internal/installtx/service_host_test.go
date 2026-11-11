package installtx

import (
	"strings"
	"testing"
)

func hostedFixture(j Journal) HostedProcess {
	ref := *j.Active
	p := HostedProcess{TxID: j.Transaction.ID, Runtime: ref, PID: 42, StartToken: "1234:0", LaunchID: strings.Repeat("c", 32)}
	p.Ready = &ReadyReceipt{Schema: 1, StoreSchema: 2, Status: "admitted", TxID: p.TxID, DaemonVersion: ref.Version,
		DaemonInstanceID: strings.Repeat("x", 24), LaunchID: p.LaunchID, CoreSHA256: strings.Repeat("a", 64), Transport: "unix", BusinessAdmission: "open", StoreRevision: 7}
	return p
}
func nextHostJournal() (Journal, HostedProcess) {
	j := installerDisk().journal
	old := hostedFixture(j)
	candidate := old.Runtime
	candidate.Version = "0.6.2"
	candidate.ArtifactSHA256 = strings.Repeat("b", 64)
	j.Generation++
	j.Pending = &candidate
	j.Transaction = &Transaction{ID: "it1_" + strings.Repeat("d", 32), Action: "install", Phase: "staged", Trust: "release", PriorActive: j.Active, Candidate: &candidate}
	return j, old
}

func TestServiceHostPendingKeepsOnlyAnAlreadyIdentifiedAdmittedOldProcess(t *testing.T) {
	j, old := nextHostJournal()
	plan, err := PlanServiceHost(j, &old)
	if err != nil || plan.Action != "keep_current" || plan.OperationalCommitID != "" || plan.Runtime == nil || *plan.Runtime != old.Runtime {
		t.Fatal("staging stopped old process or invented operational authority", plan, err)
	}
	if plan, err := PlanServiceHost(j, nil); err == nil || plan.Action != "inactive" {
		t.Fatal("pending host guessed an old operational grant", plan, err)
	}
	old.Ready = nil
	if plan, err := PlanServiceHost(j, &old); err == nil || plan.Action != "inactive" {
		t.Fatal("unready old process adopted", plan, err)
	}
}

func TestServiceHostClosedPhasesNeverLaunchCandidate(t *testing.T) {
	for _, phase := range []string{"fencing", "fenced", "rolling_back", "rolled_back", "blocked"} {
		t.Run(phase, func(t *testing.T) {
			j, old := nextHostJournal()
			j.Transaction.Phase = phase
			plan, err := PlanServiceHost(j, &old)
			if err != nil || plan.Action != "stop" || plan.Runtime != nil || plan.OperationalCommitID != "" {
				t.Fatal(plan, err)
			}
			plan, err = PlanServiceHost(j, nil)
			if err != nil || plan.Action != "inactive" {
				t.Fatal("closed host launched a writer", plan, err)
			}
		})
	}
	j, old := nextHostJournal()
	j.Pending = nil
	j.Active = nil
	j.Transaction.Candidate = nil
	j.Transaction.Action = "remove"
	j.Transaction.Phase = "removed"
	j.Transaction.OperationalCommitID = "oc1_" + strings.Repeat("e", 32)
	plan, err := PlanServiceHost(j, &old)
	if err != nil || plan.Action != "stop" || plan.Runtime != nil || plan.OperationalCommitID != "" {
		t.Fatal("remove restarted/admitted a candidate", plan, err)
	}
	if plan, err = PlanServiceHost(j, nil); err != nil || plan.Action != "inactive" {
		t.Fatal(plan, err)
	}
}

func TestServiceHostPostCommitReplaysSameOperationalIDAndMinimumReceipt(t *testing.T) {
	for _, phase := range []string{"schema_ready", "ready", "committed_closed", "committed_open"} {
		t.Run(phase, func(t *testing.T) {
			j, old := nextHostJournal()
			tx := j.Transaction
			tx.Phase = phase
			tx.Checkpoint = &CheckpointRef{ID: "checkpoint-1", SHA256: strings.Repeat("a", 64)}
			tx.SchemaReceipt = &SchemaReceipt{Schema: 2, Revision: 41, MigrationID: "migration-1", CanonicalSHA256: strings.Repeat("b", 64)}
			if strings.HasPrefix(phase, "committed") {
				j.Active = tx.Candidate
				j.Pending = nil
				tx.OperationalCommitID = "oc1_" + strings.Repeat("e", 32)
			}
			plan, err := PlanServiceHost(j, nil)
			if err != nil || plan.Action != "start_held" || plan.TxID != tx.ID || plan.OperationalCommitID != tx.OperationalCommitID || *plan.SchemaReceipt != *tx.SchemaReceipt {
				t.Fatal(plan, err)
			}
			// Returned plan fields are detached from the durable journal.
			plan.Runtime.Version = "mutated"
			plan.SchemaReceipt.Revision = 999
			if tx.Candidate.Version == "mutated" || tx.SchemaReceipt.Revision != 41 {
				t.Fatal("plan mutated journal")
			}
			plan, err = PlanServiceHost(j, &old)
			if err != nil || plan.Action != "stop" {
				t.Fatal("old and candidate could overlap", plan, err)
			}
			current := old
			current.TxID = tx.ID
			current.Runtime = *tx.Candidate
			current.Ready = nil
			plan, err = PlanServiceHost(j, &current)
			if err != nil || plan.Action != "keep_held" {
				t.Fatal(plan, err)
			}
			ready := *old.Ready
			ready.TxID = current.TxID
			ready.DaemonVersion = current.Runtime.Version
			ready.StoreRevision = 50
			current.Ready = &ready
			current.Ready.StoreRevision = 40
			if stalePlan, staleErr := PlanServiceHost(j, &current); staleErr == nil || stalePlan.Action != "inactive" {
				t.Fatal("stale readiness receipt bypassed the durable revision floor", stalePlan, staleErr)
			}
			current.Ready.StoreRevision = 50
			plan, err = PlanServiceHost(j, &current)
			if tx.OperationalCommitID == "" {
				if err == nil || plan.Action != "inactive" {
					t.Fatal("uncommitted candidate already open", plan, err)
				}
			} else if err != nil || plan.Action != "keep_open" || plan.OperationalCommitID != tx.OperationalCommitID {
				t.Fatal("later writes forced an old receipt equality/old runtime", plan, err)
			}
		})
	}
}

func TestServiceHostPreservesUnknownOrMalformedProcess(t *testing.T) {
	j, current := nextHostJournal()
	current.Runtime.ArtifactSHA256 = strings.Repeat("f", 64)
	if plan, err := PlanServiceHost(j, &current); err == nil || plan.Action != "inactive" {
		t.Fatal("unknown runtime was stopped or adopted", plan, err)
	}
	_, current = nextHostJournal()
	current.StartToken = "1234:1000000"
	if plan, err := PlanServiceHost(j, &current); err == nil || plan.Action != "inactive" {
		t.Fatal("ambiguous lifetime was adopted", plan, err)
	}
	for _, token := range []string{"", "0", "001", "1:00", "-1", "1:2:3", "1:1000000", "1/path", "1\n"} {
		current.StartToken = token
		if current.Validate() == nil {
			t.Fatal("malformed lifetime accepted", token)
		}
	}
}
