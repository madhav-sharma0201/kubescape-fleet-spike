# fleet-spike — architectural spike for Kubescape fleet posture aggregation

**This is not production code and is not proposed as a PR.** It is a local
spike that demonstrates the aggregation semantics behind my LFX application,
using a fake scanner so the logic is testable without any cluster.

## Run

```bash
go test ./... -count=1
go test -race ./... -count=5
go vet ./...
go run . > /tmp/fleet-report.json; echo "exit=$?"
```

## What it demonstrates

| Property | Where |
|---|---|
| Orchestration testable without clusters | `ContextScanner` seam + `fakeScanner` |
| Sequential execution, input order preserved | `TestContextScannedSequentiallyInInputOrder` |
| Validation before any cluster is contacted | `TestRunRejectsInvalidInputBeforeScanning` |
| One unreachable cluster does not erase others | `TestFirstContextFailsRemainingStillScanned` |
| Missing control is `MISSING`, never `PASS` | `TestControlMissingFromOneClusterIsMissingNotPass` |
| Regression vs improvement vs indeterminate | `TestDriftClassification` |
| Byte-identical JSON across 50 runs | `TestSerializedOutputIsDeterministic` |
| Report written before non-zero exit | `main.go` |

## Deliberate non-goals

Concurrency. SaaS aggregation. Changing existing single-cluster report
schemas. Cluster discovery. Real Kubescape types — the production
implementation would derive `ClusterScanResult` from the finalized posture
report rather than define its own.

## Open design questions (for maintainers)

1. Should `FleetReport` live in `core/pkg/fleet`, or alongside existing
   report types?
2. On an unreachable context: continue with a partial report and non-zero
   exit, or fail fast? (Spike implements continue-on-error.)
3. Should the first printer reuse existing result-printer infrastructure, or
   stay fleet-specific until the report shape stabilizes?
