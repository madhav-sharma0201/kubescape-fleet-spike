// Command fleet-spike demonstrates the aggregation model against fake
// evidence. It is a design spike, not a Kubescape build.
//
//	go run . > /tmp/fleet-report.json; echo "exit=$?"
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/madhav-sharma0201/fleet-spike/fleet"
)

type demoScanner struct{}

func (demoScanner) ScanContext(_ context.Context, kubeContext string) (fleet.ClusterScanResult, error) {
	switch kubeContext {
	case "kind-fleet-a": // baseline: hardened workload
		return fleet.ClusterScanResult{Controls: []fleet.ControlOutcome{
			{ID: "C-0016", Name: "Allow privilege escalation", Status: fleet.StatusPass},
			{ID: "C-0017", Name: "Immutable container filesystem", Status: fleet.StatusFail},
			{ID: "C-0057", Name: "Privileged container", Status: fleet.StatusPass},
			{ID: "C-0034", Name: "Automatic mapping of service account", Status: fleet.StatusPass},
		}}, nil
	case "kind-fleet-b": // drifted: privileged pod, :latest image
		return fleet.ClusterScanResult{Controls: []fleet.ControlOutcome{
			{ID: "C-0016", Name: "Allow privilege escalation", Status: fleet.StatusFail},
			{ID: "C-0017", Name: "Immutable container filesystem", Status: fleet.StatusPass},
			{ID: "C-0057", Name: "Privileged container", Status: fleet.StatusFail},
			{ID: "C-0075", Name: "Image pull policy on latest tag", Status: fleet.StatusFail},
		}}, nil
	default:
		return fleet.ClusterScanResult{}, errors.New(`context "` + kubeContext + `" not found in kubeconfig`)
	}
}

func main() {
	report, err := fleet.Run(context.Background(), demoScanner{}, fleet.Options{
		Contexts: []string{"kind-fleet-a", "kind-fleet-b", "kind-fleet-missing"},
		Baseline: "kind-fleet-a",
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}

	// The report is written before the non-zero exit: a failed context must
	// never cost the user the evidence already gathered.
	if report.Failed() {
		fmt.Fprintf(os.Stderr, "%d of %d contexts failed to scan\n",
			report.Summary.ContextsFailed, report.Summary.ContextsRequested)
		os.Exit(1)
	}
}
