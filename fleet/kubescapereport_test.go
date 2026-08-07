package fleet

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The testdata files are real `kubescape scan --format json` output captured
// from three kind clusters, trimmed to the fields the aggregation reads.
// fleet-a and fleet-c are configured identically; fleet-b differs by one
// privileged workload.
var realPaths = map[string]string{
	"kind-fleet-a": "testdata/kind-fleet-a.json",
	"kind-fleet-b": "testdata/kind-fleet-b.json",
	"kind-fleet-c": "testdata/kind-fleet-c.json",
}

func realScanner() *ReportScanner { return &ReportScanner{Paths: realPaths} }

// "passed" in real output is three-valued. This is the finding that drives the
// shape of ControlOutcome: a cell carrying only a status string cannot tell
// these apart.
func TestRealReport_PassedIsThreeValued(t *testing.T) {
	res, err := ParseReportFile("kind-fleet-a", realPaths["kind-fleet-a"])
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	var irrelevant, withExceptions, plain, vacuous int
	for _, c := range res.Controls {
		if c.Status != StatusPass {
			continue
		}
		switch c.SubStatus {
		case "irrelevant":
			irrelevant++
		case "w/exceptions":
			withExceptions++
		case "":
			plain++
		}
		if c.Vacuous() {
			vacuous++
		}
	}

	if irrelevant != 6 || withExceptions != 1 || plain != 9 {
		t.Errorf("passed breakdown = irrelevant:%d w/exceptions:%d plain:%d, want 6/1/9",
			irrelevant, withExceptions, plain)
	}
	// Every "irrelevant" pass examined zero resources: a pass that checked
	// nothing, which must not read the same as a pass over real evidence.
	if vacuous != 6 {
		t.Errorf("vacuous passes = %d, want 6", vacuous)
	}
}

// Cluster identity is not recoverable from the report, so the orchestrator has
// to carry the requested context through.
func TestRealReport_CarriesNoClusterIdentity(t *testing.T) {
	for ctxName, path := range realPaths {
		var rep postureReport
		f, err := os.Open(path)
		if err != nil {
			t.Fatalf("open %s: %v", path, err)
		}
		defer f.Close()
		if err := json.NewDecoder(f).Decode(&rep); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		if rep.ClusterName != "" {
			t.Errorf("%s: clusterName = %q, expected empty in captured reports", ctxName, rep.ClusterName)
		}
	}
}

// The whole fleet differs by exactly one control, and the baseline side of that
// difference is a qualified pass rather than a plain one.
func TestRealFleet_SingleDriftIsQualifiedPass(t *testing.T) {
	rep, err := Run(context.Background(), realScanner(), Options{
		Contexts: []string{"kind-fleet-a", "kind-fleet-b", "kind-fleet-c"},
		Baseline: "kind-fleet-a",
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if rep.Summary.ContextsScanned != 3 || rep.Summary.ContextsFailed != 0 {
		t.Fatalf("summary = %+v, want 3 scanned / 0 failed", rep.Summary)
	}
	if rep.Summary.ControlsTotal != 48 {
		t.Errorf("ControlsTotal = %d, want 48", rep.Summary.ControlsTotal)
	}

	var regressions []DriftFinding
	for _, d := range rep.Drift {
		if d.Kind == DriftRegression {
			regressions = append(regressions, d)
		}
	}
	if len(regressions) != 1 {
		t.Fatalf("regressions = %d, want exactly 1: %+v", len(regressions), regressions)
	}

	got := regressions[0]
	if got.ControlID != "C-0057" || got.Context != "kind-fleet-b" {
		t.Errorf("regression = %s in %s, want C-0057 in kind-fleet-b", got.ControlID, got.Context)
	}

	// 47 of 48 controls agree. Drift has to be surfaced separately from the
	// full matrix or the one row that matters is buried.
	if len(rep.Matrix) != 48 {
		t.Errorf("matrix rows = %d, want 48", len(rep.Matrix))
	}

	// The baseline for that control passed *with exceptions*, not plainly.
	base, err := ParseReportFile("kind-fleet-a", realPaths["kind-fleet-a"])
	if err != nil {
		t.Fatalf("parse baseline: %v", err)
	}
	for _, c := range base.Controls {
		if c.ID == "C-0057" {
			if c.SubStatus != "w/exceptions" {
				t.Errorf("C-0057 baseline subStatus = %q, want \"w/exceptions\"", c.SubStatus)
			}
		}
	}
}

// Identically configured clusters must produce no drift at all.
func TestRealFleet_IdenticalClustersDoNotDrift(t *testing.T) {
	rep, err := Run(context.Background(), realScanner(), Options{
		Contexts: []string{"kind-fleet-a", "kind-fleet-c"},
		Baseline: "kind-fleet-a",
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, d := range rep.Drift {
		if d.Kind == DriftRegression || d.Kind == DriftImprovement {
			t.Errorf("unexpected drift between identical clusters: %+v", d)
		}
	}
}

// An unreachable context must be recorded and must not cost the fleet the
// results of the contexts that did scan. This path only exists because
// Kubescape.Scan returns an error for an unreachable cluster rather than
// exiting the process (kubescape/kubescape#2788).
func TestRealFleet_UnreachableContextDoesNotAbortFleet(t *testing.T) {
	rep, err := Run(context.Background(), realScanner(), Options{
		Contexts: []string{"kind-fleet-a", "kind-fleet-missing", "kind-fleet-b"},
		Baseline: "kind-fleet-a",
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if rep.Summary.ContextsScanned != 2 || rep.Summary.ContextsFailed != 1 {
		t.Errorf("summary = %+v, want 2 scanned / 1 failed", rep.Summary)
	}
	if !rep.Failed() {
		t.Error("Failed() must be true so the command can exit non-zero")
	}
	if len(rep.Errors) != 1 || rep.Errors[0].Context != "kind-fleet-missing" {
		t.Fatalf("errors = %+v, want one for kind-fleet-missing", rep.Errors)
	}
	if !strings.Contains(rep.Errors[0].Message, "not reachable") {
		t.Errorf("error message = %q, want it to explain unreachability", rep.Errors[0].Message)
	}

	// The context that failed must still appear in the matrix, as SCAN_ERROR
	// rather than silently absent or, worse, passing.
	for _, row := range rep.Matrix {
		var found bool
		for _, cell := range row.Cells {
			if cell.Context == "kind-fleet-missing" {
				found = true
				if cell.Status != StatusScanError {
					t.Fatalf("%s: unreachable cell = %s, want SCAN_ERROR", row.ControlID, cell.Status)
				}
			}
		}
		if !found {
			t.Fatalf("%s: unreachable context missing from matrix row", row.ControlID)
		}
	}
}

// Aggregating the same evidence twice must produce byte-identical JSON.
func TestRealFleet_OutputIsDeterministic(t *testing.T) {
	opts := Options{
		Contexts: []string{"kind-fleet-c", "kind-fleet-a", "kind-fleet-b"},
		Baseline: "kind-fleet-a",
	}
	first, err := Run(context.Background(), realScanner(), opts)
	if err != nil {
		t.Fatalf("run 1: %v", err)
	}
	second, err := Run(context.Background(), realScanner(), opts)
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}

	a, _ := json.Marshal(first)
	b, _ := json.Marshal(second)
	if string(a) != string(b) {
		t.Error("two runs over the same evidence produced different JSON")
	}

	// Columns follow the requested context order, not sorted order.
	want := []string{"kind-fleet-c", "kind-fleet-a", "kind-fleet-b"}
	for i, c := range first.Contexts {
		if c != want[i] {
			t.Fatalf("contexts = %v, want requested order %v", first.Contexts, want)
		}
	}
}
