// Command fleet-spike aggregates real Kubescape posture reports across several
// kube contexts and prints a FleetReport.
//
// The evidence is genuine: fleet/testdata holds `kubescape scan --format json`
// output captured from three kind clusters, trimmed to the fields the
// aggregation reads. kind-fleet-a and kind-fleet-c are configured identically;
// kind-fleet-b differs by one privileged workload. kind-fleet-missing has no
// report, so it exercises the unreachable-context path.
//
//	go run . > /tmp/fleet-report.json; echo "exit=$?"
//	go run . -table
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/madhav-sharma0201/fleet-spike/fleet"
)

func main() {
	asTable := flag.Bool("table", false, "render the control matrix as a table instead of JSON")
	flag.Parse()

	scanner := &fleet.ReportScanner{Paths: map[string]string{
		"kind-fleet-a": "fleet/testdata/kind-fleet-a.json",
		"kind-fleet-b": "fleet/testdata/kind-fleet-b.json",
		"kind-fleet-c": "fleet/testdata/kind-fleet-c.json",
	}}

	report, err := fleet.Run(context.Background(), scanner, fleet.Options{
		Contexts: []string{"kind-fleet-a", "kind-fleet-b", "kind-fleet-c", "kind-fleet-missing"},
		Baseline: "kind-fleet-a",
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}

	if *asTable {
		renderTable(report)
	} else {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(2)
		}
	}

	// The report is written before the non-zero exit: a failed context must
	// never cost the user the evidence already gathered.
	if report.Failed() {
		fmt.Fprintf(os.Stderr, "\n%d of %d contexts failed to scan\n",
			report.Summary.ContextsFailed, report.Summary.ContextsRequested)
		os.Exit(1)
	}
}

// renderTable prints drift first and the full matrix second. With 48 controls
// and one disagreement, a report that leads with the grid buries the only row
// anyone needs to read.
func renderTable(r *fleet.FleetReport) {
	fmt.Printf("baseline: %s\n", r.Baseline)
	fmt.Printf("contexts: %s\n", strings.Join(r.Contexts, ", "))
	fmt.Printf("%d controls, %d scanned, %d failed, %d regressions\n\n",
		r.Summary.ControlsTotal, r.Summary.ContextsScanned,
		r.Summary.ContextsFailed, r.Summary.Regressions)

	if len(r.Errors) > 0 {
		fmt.Println("ERRORS")
		for _, e := range r.Errors {
			fmt.Printf("  %-22s %s\n", e.Context, e.Message)
		}
		fmt.Println()
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

	// Drift is split by whether it is actionable. An unreachable context makes
	// every control indeterminate against the baseline; printing all 48 of those
	// buries the one row that changed. Count them instead.
	var actionable []fleet.DriftFinding
	indeterminate := map[string]int{}
	for _, d := range r.Drift {
		switch d.Kind {
		case fleet.DriftRegression, fleet.DriftImprovement:
			actionable = append(actionable, d)
		case fleet.DriftIndeterminate:
			indeterminate[d.Context]++
		}
	}

	fmt.Println("DRIFT (vs baseline)")
	if len(actionable) == 0 {
		fmt.Println("  none")
	} else {
		fmt.Fprintln(w, "  CONTROL\tCONTEXT\tBASELINE\tTARGET\tKIND")
		for _, d := range actionable {
			fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\n",
				d.ControlID, d.Context, d.Baseline, d.Target, d.Kind)
		}
		w.Flush()
	}
	failed := map[string]bool{}
	for _, e := range r.Errors {
		failed[e.Context] = true
	}
	for _, ctx := range r.Contexts {
		n := indeterminate[ctx]
		if n == 0 {
			continue
		}
		// Two different reasons produce INDETERMINATE, and conflating them is
		// the mistake this status exists to prevent: the context never scanned,
		// or it scanned and the control produced no usable evidence.
		reason := "no usable evidence on one side (skipped or absent)"
		if failed[ctx] {
			reason = "context did not scan"
		}
		fmt.Printf("  %s: %d indeterminate — %s\n", ctx, n, reason)
	}

	fmt.Println("\nMATRIX")
	header := []string{"  CONTROL"}
	header = append(header, r.Contexts...)
	fmt.Fprintln(w, strings.Join(header, "\t"))
	for _, row := range r.Matrix {
		cells := []string{"  " + row.ControlID}
		for _, c := range row.Cells {
			cells = append(cells, string(c.Status))
		}
		fmt.Fprintln(w, strings.Join(cells, "\t"))
	}
	w.Flush()
}
