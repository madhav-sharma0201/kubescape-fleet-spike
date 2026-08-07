package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
)

// This file adapts real Kubescape output into the spike's model. Everything
// here is derived from actual `kubescape scan --format json` reports captured
// from three kind clusters, not from a hand-written fixture, so the status
// vocabulary below is the one the tool really emits.

// postureReport is the subset of Kubescape's JSON report the aggregation needs.
// The full report is ~850KB per cluster and mostly per-resource detail.
type postureReport struct {
	ClusterName    string `json:"clusterName"`
	SummaryDetails struct {
		Controls map[string]reportControl `json:"controls"`
	} `json:"summaryDetails"`
}

type reportControl struct {
	ControlID  string `json:"controlID"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	StatusInfo struct {
		Status    string `json:"status"`
		SubStatus string `json:"subStatus"`
	} `json:"statusInfo"`
	ResourceCounters ResourceCounters `json:"ResourceCounters"`
}

// Kubescape's own status vocabulary, as observed in real reports.
const (
	ksStatusPassed  = "passed"
	ksStatusFailed  = "failed"
	ksStatusSkipped = "skipped"
)

// toControlStatus maps Kubescape's status onto the spike's model.
//
// "skipped" deliberately becomes StatusNotEvaluated rather than StatusPass:
// a skipped control produced no posture evidence, and treating absence of
// evidence as a pass is the failure mode this spike exists to avoid.
func toControlStatus(status string) ControlStatus {
	switch status {
	case ksStatusPassed:
		return StatusPass
	case ksStatusFailed:
		return StatusFail
	case ksStatusSkipped:
		return StatusNotEvaluated
	default:
		return StatusNotEvaluated
	}
}

// ParseReport reads one Kubescape JSON posture report.
//
// The context name must be supplied by the caller: in every report captured
// from these clusters, clusterName was the empty string, so cluster identity
// is not recoverable from the report itself and the orchestrator has to carry
// the requested kube-context through.
func ParseReport(context string, r io.Reader) (ClusterScanResult, error) {
	var rep postureReport
	if err := json.NewDecoder(r).Decode(&rep); err != nil {
		return ClusterScanResult{}, fmt.Errorf("decode posture report for %q: %w", context, err)
	}
	if len(rep.SummaryDetails.Controls) == 0 {
		return ClusterScanResult{}, fmt.Errorf("posture report for %q contains no controls", context)
	}

	out := ClusterScanResult{
		Context:  context,
		Controls: make([]ControlOutcome, 0, len(rep.SummaryDetails.Controls)),
	}
	for id, c := range rep.SummaryDetails.Controls {
		controlID := c.ControlID
		if controlID == "" {
			controlID = id
		}
		out.Controls = append(out.Controls, ControlOutcome{
			ID:        controlID,
			Name:      c.Name,
			Status:    toControlStatus(c.Status),
			SubStatus: c.StatusInfo.SubStatus,
			Counters:  c.ResourceCounters,
		})
	}
	// Map iteration is random; sort so the loader itself cannot be a source of
	// non-determinism in the report.
	sort.Slice(out.Controls, func(i, j int) bool { return out.Controls[i].ID < out.Controls[j].ID })
	return out, nil
}

// ParseReportFile reads a report from disk.
func ParseReportFile(context, path string) (ClusterScanResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return ClusterScanResult{}, fmt.Errorf("open posture report for %q: %w", context, err)
	}
	defer f.Close()
	return ParseReport(context, f)
}

// ReportScanner serves previously captured posture reports, one per context.
// It lets the orchestrator run over real Kubescape output with no cluster
// present. A context with no configured report is reported as unreachable,
// which is how the unreachable-context path gets exercised.
type ReportScanner struct {
	// Paths maps kube-context name to a posture report file.
	Paths map[string]string
}

// ScanContext implements ContextScanner.
func (s *ReportScanner) ScanContext(_ context.Context, kubeContext string) (ClusterScanResult, error) {
	path, ok := s.Paths[kubeContext]
	if !ok {
		return ClusterScanResult{}, fmt.Errorf("context %q is not reachable: no kubeconfig entry", kubeContext)
	}
	return ParseReportFile(kubeContext, path)
}
