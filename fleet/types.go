// Package fleet is an architectural spike for multi-cluster posture
// aggregation in Kubescape. It is deliberately decoupled from Kubescape's
// production types: the orchestrator depends only on the ContextScanner
// interface, so the aggregation logic is testable without any cluster.
//
// This is not production code. It exists to demonstrate that the failure
// semantics, status model and determinism guarantees have been thought
// through before any implementation PR is opened.
package fleet

import "fmt"

// ControlStatus is the evaluation outcome of a single control in a single
// cluster.
//
// The distinction between MISSING, NotEvaluated and ScanError is the point of
// this type. Collapsing them into PASS creates false confidence; collapsing
// them into FAIL destroys diagnostic meaning. Both are worse than admitting
// that the evidence is absent.
type ControlStatus string

const (
	// StatusPass means the control was evaluated and passed.
	StatusPass ControlStatus = "PASS"
	// StatusFail means the control was evaluated and failed.
	StatusFail ControlStatus = "FAIL"
	// StatusNotEvaluated means the control was present in the cluster's
	// result set but could not be evaluated (for example, no matching
	// resources, or the control was skipped).
	StatusNotEvaluated ControlStatus = "NOT_EVALUATED"
	// StatusMissing means the control did not appear at all in this
	// cluster's result set, though it appeared in at least one other
	// cluster. Usually a framework or version mismatch.
	StatusMissing ControlStatus = "MISSING"
	// StatusScanError means the entire scan of this context failed, so no
	// control-level evidence exists for it.
	StatusScanError ControlStatus = "SCAN_ERROR"
)

// Evaluated reports whether the status carries usable posture evidence.
func (s ControlStatus) Evaluated() bool {
	return s == StatusPass || s == StatusFail
}

// DriftKind classifies a target cluster's status relative to the baseline.
type DriftKind string

const (
	// DriftNone means baseline and target agree (PASS/PASS or FAIL/FAIL).
	DriftNone DriftKind = "NO_DRIFT"
	// DriftRegression means the baseline passes and the target fails. This
	// is the finding operators actually care about.
	DriftRegression DriftKind = "REGRESSION"
	// DriftImprovement means the baseline fails and the target passes.
	DriftImprovement DriftKind = "IMPROVEMENT"
	// DriftIndeterminate means at least one side lacks usable evidence.
	// It is reported explicitly rather than silently dropped.
	DriftIndeterminate DriftKind = "INDETERMINATE"
)

// ControlOutcome is one control's result within one cluster.
type ControlOutcome struct {
	ID     string        `json:"controlID"`
	Name   string        `json:"name,omitempty"`
	Status ControlStatus `json:"status"`
}

// ClusterScanResult is the normalized result of scanning a single context.
// In production this would be derived from the finalized posture report
// rather than defined here.
type ClusterScanResult struct {
	Context  string           `json:"context"`
	Controls []ControlOutcome `json:"controls"`
}

// ContextError records a per-context failure as first-class report data. A
// failed context must never erase the results of successful contexts.
type ContextError struct {
	Context string `json:"context"`
	Message string `json:"message"`
}

// Cell is one control-by-cluster intersection in the matrix.
type Cell struct {
	Context string        `json:"context"`
	Status  ControlStatus `json:"status"`
}

// ControlMatrixRow is one control across every context, in input order.
type ControlMatrixRow struct {
	ControlID string `json:"controlID"`
	Name      string `json:"name,omitempty"`
	Cells     []Cell `json:"cells"`
}

// DriftFinding is one control's classification for one non-baseline context.
type DriftFinding struct {
	ControlID string        `json:"controlID"`
	Name      string        `json:"name,omitempty"`
	Context   string        `json:"context"`
	Baseline  ControlStatus `json:"baselineStatus"`
	Target    ControlStatus `json:"targetStatus"`
	Kind      DriftKind     `json:"kind"`
}

// FleetSummary is a small, stable header suitable for CI assertions.
type FleetSummary struct {
	ContextsRequested int `json:"contextsRequested"`
	ContextsScanned   int `json:"contextsScanned"`
	ContextsFailed    int `json:"contextsFailed"`
	ControlsTotal     int `json:"controlsTotal"`
	Regressions       int `json:"regressions"`
	Improvements      int `json:"improvements"`
	Indeterminate     int `json:"indeterminate"`
}

// FleetReport is the serialized output of a fleet scan.
//
// Field ordering is fixed, Contexts preserves user input order, and Matrix
// and Drift are sorted deterministically, so two runs over the same evidence
// produce byte-identical JSON.
type FleetReport struct {
	Baseline string             `json:"baseline"`
	Contexts []string           `json:"contexts"`
	Summary  FleetSummary       `json:"summary"`
	Matrix   []ControlMatrixRow `json:"matrix"`
	Drift    []DriftFinding     `json:"drift"`
	Errors   []ContextError     `json:"errors"`
}

// Failed reports whether any context failed to scan. The command should exit
// non-zero when this is true, after the report has been written.
func (r *FleetReport) Failed() bool { return len(r.Errors) > 0 }

// ValidationError is returned before any scanning begins. Invalid input must
// never cost the user a partial multi-cluster scan.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("invalid %s: %s", e.Field, e.Message)
}
