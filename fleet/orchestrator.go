package fleet

import (
	"context"
	"sort"
)

// ContextScanner scans exactly one Kubernetes context. Production
// implementation wraps Kubescape's existing single-cluster scan path; tests
// use a fake. This seam is the whole reason the aggregation logic can be
// tested without a cluster.
type ContextScanner interface {
	ScanContext(ctx context.Context, kubeContext string) (ClusterScanResult, error)
}

// Options configures a fleet run.
type Options struct {
	// Contexts is the ordered list of kube contexts to scan. Order is
	// preserved throughout the report.
	Contexts []string
	// Baseline is the context that drift is measured against. When empty,
	// the first context is used.
	Baseline string
}

// Validate checks the request before any cluster is contacted.
func (o *Options) Validate() error {
	if len(o.Contexts) == 0 {
		return &ValidationError{Field: "contexts", Message: "at least one context is required"}
	}
	seen := make(map[string]struct{}, len(o.Contexts))
	for _, c := range o.Contexts {
		if c == "" {
			return &ValidationError{Field: "contexts", Message: "context name must not be empty"}
		}
		if _, dup := seen[c]; dup {
			return &ValidationError{Field: "contexts", Message: "duplicate context: " + c}
		}
		seen[c] = struct{}{}
	}
	if o.Baseline == "" {
		return nil
	}
	if _, ok := seen[o.Baseline]; !ok {
		return &ValidationError{Field: "baseline", Message: "baseline context is not in --contexts: " + o.Baseline}
	}
	return nil
}

// baseline returns the effective baseline context.
func (o *Options) baseline() string {
	if o.Baseline != "" {
		return o.Baseline
	}
	return o.Contexts[0]
}

// Run scans every context sequentially and aggregates the results.
//
// Semantics, chosen deliberately:
//
//   - Sequential by construction. Kubescape carries process-wide scan state,
//     so parallelism is out of scope until that state is proven isolated.
//   - Continue on error. One unreachable cluster must not discard the
//     evidence already gathered from reachable ones.
//   - Cancellation stops further scans; contexts not reached are recorded as
//     errors rather than silently omitted.
//   - Run returns an error only for invalid input. Scan failures are report
//     data, and the caller sets the exit code from FleetReport.Failed.
func Run(ctx context.Context, scanner ContextScanner, opts Options) (*FleetReport, error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}

	baseline := opts.baseline()
	results := make(map[string]ClusterScanResult, len(opts.Contexts))
	var errs []ContextError

	for i, kubeContext := range opts.Contexts {
		if err := ctx.Err(); err != nil {
			for _, remaining := range opts.Contexts[i:] {
				errs = append(errs, ContextError{
					Context: remaining,
					Message: "not scanned: " + err.Error(),
				})
			}
			break
		}

		res, err := scanner.ScanContext(ctx, kubeContext)
		if err != nil {
			errs = append(errs, ContextError{Context: kubeContext, Message: err.Error()})
			continue
		}
		res.Context = kubeContext
		results[kubeContext] = res
	}

	return aggregate(opts.Contexts, baseline, results, errs), nil
}

// aggregate is pure: given per-context evidence it produces a deterministic
// report. Kept separate from Run so it can be tested exhaustively.
func aggregate(contexts []string, baseline string, results map[string]ClusterScanResult, errs []ContextError) *FleetReport {
	failed := make(map[string]struct{}, len(errs))
	for _, e := range errs {
		failed[e.Context] = struct{}{}
	}

	// Union of control IDs across every successful context, plus the best
	// available display name for each.
	names := map[string]string{}
	byContext := make(map[string]map[string]ControlOutcome, len(results))
	for kubeContext, res := range results {
		m := make(map[string]ControlOutcome, len(res.Controls))
		for _, oc := range res.Controls {
			m[oc.ID] = oc
			if oc.Name != "" {
				if _, ok := names[oc.ID]; !ok {
					names[oc.ID] = oc.Name
				}
			}
		}
		byContext[kubeContext] = m
	}

	ids := make([]string, 0, len(names))
	seenID := map[string]struct{}{}
	for _, m := range byContext {
		for id := range m {
			if _, ok := seenID[id]; !ok {
				seenID[id] = struct{}{}
				ids = append(ids, id)
			}
		}
	}
	// Sorted by control ID: input map iteration order must not reach output.
	sort.Strings(ids)

	statusOf := func(kubeContext, id string) ControlStatus {
		if _, bad := failed[kubeContext]; bad {
			return StatusScanError
		}
		m, ok := byContext[kubeContext]
		if !ok {
			return StatusScanError
		}
		oc, ok := m[id]
		if !ok {
			return StatusMissing
		}
		if oc.Status == "" {
			return StatusNotEvaluated
		}
		return oc.Status
	}

	matrix := make([]ControlMatrixRow, 0, len(ids))
	for _, id := range ids {
		row := ControlMatrixRow{ControlID: id, Name: names[id], Cells: make([]Cell, 0, len(contexts))}
		for _, kubeContext := range contexts {
			row.Cells = append(row.Cells, Cell{Context: kubeContext, Status: statusOf(kubeContext, id)})
		}
		matrix = append(matrix, row)
	}

	drift := make([]DriftFinding, 0)
	summary := FleetSummary{
		ContextsRequested: len(contexts),
		ContextsScanned:   len(results),
		ContextsFailed:    len(errs),
		ControlsTotal:     len(ids),
	}

	for _, id := range ids {
		base := statusOf(baseline, id)
		for _, kubeContext := range contexts {
			if kubeContext == baseline {
				continue
			}
			target := statusOf(kubeContext, id)
			kind := classify(base, target)
			switch kind {
			case DriftRegression:
				summary.Regressions++
			case DriftImprovement:
				summary.Improvements++
			case DriftIndeterminate:
				summary.Indeterminate++
			}
			if kind == DriftNone {
				continue
			}
			drift = append(drift, DriftFinding{
				ControlID: id,
				Name:      names[id],
				Context:   kubeContext,
				Baseline:  base,
				Target:    target,
				Kind:      kind,
			})
		}
	}

	if errs == nil {
		errs = []ContextError{}
	}
	sort.SliceStable(errs, func(i, j int) bool { return errs[i].Context < errs[j].Context })

	return &FleetReport{
		Baseline: baseline,
		Contexts: append([]string(nil), contexts...),
		Summary:  summary,
		Matrix:   matrix,
		Drift:    drift,
		Errors:   errs,
	}
}

// classify compares a target status to the baseline status.
//
// Indeterminate is returned whenever either side lacks usable evidence. That
// is the conservative choice: an unreachable cluster is not a passing cluster.
func classify(base, target ControlStatus) DriftKind {
	if !base.Evaluated() || !target.Evaluated() {
		return DriftIndeterminate
	}
	switch {
	case base == StatusPass && target == StatusFail:
		return DriftRegression
	case base == StatusFail && target == StatusPass:
		return DriftImprovement
	default:
		return DriftNone
	}
}
