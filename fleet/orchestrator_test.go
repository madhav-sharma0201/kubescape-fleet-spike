package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// fakeScanner records call order and returns canned evidence. It exists to
// prove the orchestrator is testable without a Kubernetes cluster.
type fakeScanner struct {
	results map[string][]ControlOutcome
	errs    map[string]error
	calls   []string
	onCall  func(kubeContext string)
}

func (f *fakeScanner) ScanContext(_ context.Context, kubeContext string) (ClusterScanResult, error) {
	f.calls = append(f.calls, kubeContext)
	if f.onCall != nil {
		f.onCall(kubeContext)
	}
	if err, ok := f.errs[kubeContext]; ok {
		return ClusterScanResult{}, err
	}
	return ClusterScanResult{Context: kubeContext, Controls: f.results[kubeContext]}, nil
}

func pass(id string) ControlOutcome { return ControlOutcome{ID: id, Name: id, Status: StatusPass} }
func fail(id string) ControlOutcome { return ControlOutcome{ID: id, Name: id, Status: StatusFail} }
func notEval(id string) ControlOutcome {
	return ControlOutcome{ID: id, Name: id, Status: StatusNotEvaluated}
}

func cellStatus(t *testing.T, r *FleetReport, controlID, kubeContext string) ControlStatus {
	t.Helper()
	for _, row := range r.Matrix {
		if row.ControlID != controlID {
			continue
		}
		for _, c := range row.Cells {
			if c.Context == kubeContext {
				return c.Status
			}
		}
		t.Fatalf("context %q absent from row %q", kubeContext, controlID)
	}
	t.Fatalf("control %q absent from matrix", controlID)
	return ""
}

func driftKind(t *testing.T, r *FleetReport, controlID, kubeContext string) DriftKind {
	t.Helper()
	for _, d := range r.Drift {
		if d.ControlID == controlID && d.Context == kubeContext {
			return d.Kind
		}
	}
	return DriftNone
}

// --- validation -------------------------------------------------------------

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		opts    Options
		wantErr bool
	}{
		{"two valid contexts", Options{Contexts: []string{"a", "b"}}, false},
		{"explicit valid baseline", Options{Contexts: []string{"a", "b"}, Baseline: "b"}, false},
		{"no contexts", Options{}, true},
		{"empty context value", Options{Contexts: []string{"a", ""}}, true},
		{"duplicate context", Options{Contexts: []string{"a", "b", "a"}}, true},
		{"baseline not in contexts", Options{Contexts: []string{"a", "b"}, Baseline: "c"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.opts.Validate()
			if tc.wantErr != (err != nil) {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestRunRejectsInvalidInputBeforeScanning(t *testing.T) {
	f := &fakeScanner{}
	if _, err := Run(context.Background(), f, Options{Contexts: []string{"a", "a"}}); err == nil {
		t.Fatal("expected validation error")
	}
	if len(f.calls) != 0 {
		t.Fatalf("scanner was called %d times on invalid input; want 0", len(f.calls))
	}
}

// --- ordering and defaults --------------------------------------------------

func TestContextsScannedSequentiallyInInputOrder(t *testing.T) {
	f := &fakeScanner{results: map[string][]ControlOutcome{
		"c": {pass("C-0001")}, "a": {pass("C-0001")}, "b": {pass("C-0001")},
	}}
	r, err := Run(context.Background(), f, Options{Contexts: []string{"c", "a", "b"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"c", "a", "b"}
	for i := range want {
		if f.calls[i] != want[i] {
			t.Fatalf("scan order = %v, want %v", f.calls, want)
		}
	}
	if r.Contexts[0] != "c" {
		t.Fatalf("report context order not preserved: %v", r.Contexts)
	}
	if r.Baseline != "c" {
		t.Fatalf("default baseline = %q, want first context %q", r.Baseline, "c")
	}
}

// --- partial failure --------------------------------------------------------

func TestFirstContextFailsRemainingStillScanned(t *testing.T) {
	f := &fakeScanner{
		errs:    map[string]error{"a": errors.New("dial tcp: connection refused")},
		results: map[string][]ControlOutcome{"b": {pass("C-0001")}, "c": {fail("C-0001")}},
	}
	r, err := Run(context.Background(), f, Options{Contexts: []string{"a", "b", "c"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 3 {
		t.Fatalf("scanned %d contexts, want 3", len(f.calls))
	}
	if got := cellStatus(t, r, "C-0001", "a"); got != StatusScanError {
		t.Fatalf("failed context cell = %q, want SCAN_ERROR", got)
	}
	if got := cellStatus(t, r, "C-0001", "b"); got != StatusPass {
		t.Fatalf("healthy context lost its result: %q", got)
	}
	if !r.Failed() {
		t.Fatal("report should be marked failed")
	}
}

func TestMiddleContextFailsProducesPartialReport(t *testing.T) {
	f := &fakeScanner{
		errs:    map[string]error{"b": errors.New("unauthorized")},
		results: map[string][]ControlOutcome{"a": {pass("C-0001")}, "c": {pass("C-0001")}},
	}
	r, _ := Run(context.Background(), f, Options{Contexts: []string{"a", "b", "c"}})
	if r.Summary.ContextsScanned != 2 || r.Summary.ContextsFailed != 1 {
		t.Fatalf("summary = %+v", r.Summary)
	}
	if len(r.Errors) != 1 || r.Errors[0].Context != "b" {
		t.Fatalf("errors = %+v", r.Errors)
	}
}

func TestEveryContextFails(t *testing.T) {
	f := &fakeScanner{errs: map[string]error{
		"a": errors.New("no such context"), "b": errors.New("timeout"),
	}}
	r, err := Run(context.Background(), f, Options{Contexts: []string{"a", "b"}})
	if err != nil {
		t.Fatalf("scan failures must not be returned as a Go error: %v", err)
	}
	if len(r.Errors) != 2 {
		t.Fatalf("want 2 recorded errors, got %d", len(r.Errors))
	}
	if len(r.Matrix) != 0 {
		t.Fatalf("no evidence exists, matrix should be empty, got %d rows", len(r.Matrix))
	}
	if !r.Failed() {
		t.Fatal("command must exit non-zero")
	}
}

func TestCancellationStopsScanningAndRecordsRemaining(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := &fakeScanner{
		results: map[string][]ControlOutcome{"a": {pass("C-0001")}},
		onCall: func(kubeContext string) {
			if kubeContext == "a" {
				cancel()
			}
		},
	}
	r, err := Run(ctx, f, Options{Contexts: []string{"a", "b", "c"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("scanning continued after cancellation: %v", f.calls)
	}
	if len(r.Errors) != 2 {
		t.Fatalf("unreached contexts must be recorded, got %+v", r.Errors)
	}
	if got := cellStatus(t, r, "C-0001", "b"); got != StatusScanError {
		t.Fatalf("unreached context cell = %q, want SCAN_ERROR", got)
	}
}

// --- matrix -----------------------------------------------------------------

func TestControlMissingFromOneClusterIsMissingNotPass(t *testing.T) {
	f := &fakeScanner{results: map[string][]ControlOutcome{
		"a": {pass("C-0001"), pass("C-0002")},
		"b": {pass("C-0001")},
	}}
	r, _ := Run(context.Background(), f, Options{Contexts: []string{"a", "b"}})
	if got := cellStatus(t, r, "C-0002", "b"); got != StatusMissing {
		t.Fatalf("absent control = %q, want MISSING", got)
	}
	if got := driftKind(t, r, "C-0002", "b"); got != DriftIndeterminate {
		t.Fatalf("missing target drift = %q, want INDETERMINATE", got)
	}
}

func TestControlOnlyInTargetIsMissingInBaseline(t *testing.T) {
	f := &fakeScanner{results: map[string][]ControlOutcome{
		"a": {pass("C-0001")},
		"b": {pass("C-0001"), fail("C-0009")},
	}}
	r, _ := Run(context.Background(), f, Options{Contexts: []string{"a", "b"}, Baseline: "a"})
	if got := cellStatus(t, r, "C-0009", "a"); got != StatusMissing {
		t.Fatalf("baseline cell = %q, want MISSING", got)
	}
	if got := driftKind(t, r, "C-0009", "b"); got != DriftIndeterminate {
		t.Fatalf("drift = %q, want INDETERMINATE (no baseline evidence)", got)
	}
}

func TestMatrixSortedByControlIDRegardlessOfInputOrder(t *testing.T) {
	f := &fakeScanner{results: map[string][]ControlOutcome{
		"a": {pass("C-0055"), pass("C-0002"), pass("C-0013")},
	}}
	r, _ := Run(context.Background(), f, Options{Contexts: []string{"a"}})
	want := []string{"C-0002", "C-0013", "C-0055"}
	for i, id := range want {
		if r.Matrix[i].ControlID != id {
			t.Fatalf("matrix order = %v", r.Matrix)
		}
	}
}

func TestEmptyScanResultDoesNotPanic(t *testing.T) {
	f := &fakeScanner{results: map[string][]ControlOutcome{"a": {}, "b": nil}}
	r, err := Run(context.Background(), f, Options{Contexts: []string{"a", "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Matrix) != 0 || len(r.Drift) != 0 {
		t.Fatalf("expected empty report, got %+v", r)
	}
	if r.Failed() {
		t.Fatal("empty results are not a failure")
	}
}

// --- drift ------------------------------------------------------------------

func TestDriftClassification(t *testing.T) {
	cases := []struct {
		name         string
		base, target ControlOutcome
		want         DriftKind
	}{
		{"pass to fail is regression", pass("C-0001"), fail("C-0001"), DriftRegression},
		{"fail to pass is improvement", fail("C-0001"), pass("C-0001"), DriftImprovement},
		{"pass to pass is no drift", pass("C-0001"), pass("C-0001"), DriftNone},
		{"fail to fail is no drift", fail("C-0001"), fail("C-0001"), DriftNone},
		{"not evaluated target", pass("C-0001"), notEval("C-0001"), DriftIndeterminate},
		{"not evaluated baseline", notEval("C-0001"), fail("C-0001"), DriftIndeterminate},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeScanner{results: map[string][]ControlOutcome{
				"base": {tc.base}, "target": {tc.target},
			}}
			r, _ := Run(context.Background(), f, Options{
				Contexts: []string{"base", "target"}, Baseline: "base",
			})
			if got := driftKind(t, r, "C-0001", "target"); got != tc.want {
				t.Fatalf("drift = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBaselineIsNotComparedToItself(t *testing.T) {
	f := &fakeScanner{results: map[string][]ControlOutcome{
		"a": {fail("C-0001")}, "b": {fail("C-0001")},
	}}
	r, _ := Run(context.Background(), f, Options{Contexts: []string{"a", "b"}, Baseline: "a"})
	for _, d := range r.Drift {
		if d.Context == "a" {
			t.Fatalf("baseline compared against itself: %+v", d)
		}
	}
}

func TestSummaryCountsDrift(t *testing.T) {
	f := &fakeScanner{results: map[string][]ControlOutcome{
		"a": {pass("C-0001"), fail("C-0002"), pass("C-0003")},
		"b": {fail("C-0001"), pass("C-0002"), notEval("C-0003")},
	}}
	r, _ := Run(context.Background(), f, Options{Contexts: []string{"a", "b"}, Baseline: "a"})
	if r.Summary.Regressions != 1 || r.Summary.Improvements != 1 || r.Summary.Indeterminate != 1 {
		t.Fatalf("summary = %+v", r.Summary)
	}
}

// --- determinism ------------------------------------------------------------

// TestSerializedOutputIsDeterministic guards the property that makes fleet
// reports usable in CI: identical evidence must produce identical bytes,
// regardless of Go map iteration order.
func TestSerializedOutputIsDeterministic(t *testing.T) {
	build := func() string {
		f := &fakeScanner{results: map[string][]ControlOutcome{
			"a": {pass("C-0031"), fail("C-0002"), pass("C-0017"), notEval("C-0088")},
			"b": {fail("C-0031"), fail("C-0002"), pass("C-0055")},
			"c": {pass("C-0002")},
		}}
		r, err := Run(context.Background(), f, Options{
			Contexts: []string{"a", "b", "c"}, Baseline: "a",
		})
		if err != nil {
			t.Fatal(err)
		}
		out, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	first := build()
	for i := 0; i < 50; i++ {
		if got := build(); got != first {
			t.Fatalf("non-deterministic output on run %d", i)
		}
	}
}

func TestReportMarshalsWithoutNilCollections(t *testing.T) {
	f := &fakeScanner{results: map[string][]ControlOutcome{"a": {pass("C-0001")}}}
	r, _ := Run(context.Background(), f, Options{Contexts: []string{"a"}})
	out, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"matrix", "drift", "errors", "contexts"} {
		if decoded[key] == nil {
			t.Fatalf("%q serialized as null; consumers should always see an array", key)
		}
	}
}
