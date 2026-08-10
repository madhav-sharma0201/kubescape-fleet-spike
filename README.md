# fleet-spike — architectural spike for Kubescape fleet posture aggregation

**This is not production code and is not proposed as a PR.** It is a local
spike that demonstrates the aggregation semantics behind my LFX application.

It runs two ways: against a fake scanner for synthetic edge cases, and against
**real `kubescape scan --format json` output** captured from three kind
clusters (`fleet/testdata/`, trimmed to the fields the aggregation reads). The
demo below uses the real reports.

## Run

```bash
go test ./... -count=1
go test -race ./... -count=5
go vet ./...
go run . -table                                  # matrix + drift
go run . > /tmp/fleet-report.json; echo "exit=$?"   # deterministic JSON
```

## What it produces

Run against the captured reports in `fleet/testdata/` — three real kind
contexts plus one that does not exist:

```
$ go run . -table
baseline: kind-fleet-a
contexts: kind-fleet-a, kind-fleet-b, kind-fleet-c, kind-fleet-missing
48 controls, 3 scanned, 1 failed, 1 regressions

ERRORS
  kind-fleet-missing     context "kind-fleet-missing" is not reachable: no kubeconfig entry

DRIFT (vs baseline)
  CONTROL  CONTEXT       BASELINE  TARGET  KIND
  C-0057   kind-fleet-b  PASS      FAIL    REGRESSION
  kind-fleet-b: 2 indeterminate — no usable evidence on one side (skipped or absent)
  kind-fleet-c: 2 indeterminate — no usable evidence on one side (skipped or absent)
  kind-fleet-missing: 48 indeterminate — context did not scan

MATRIX
  CONTROL  kind-fleet-a   kind-fleet-b   kind-fleet-c   kind-fleet-missing
  C-0002   FAIL           FAIL           FAIL           SCAN_ERROR
  C-0005   PASS           PASS           PASS           SCAN_ERROR
  C-0007   FAIL           FAIL           FAIL           SCAN_ERROR
  C-0012   FAIL           FAIL           FAIL           SCAN_ERROR
  C-0013   FAIL           FAIL           FAIL           SCAN_ERROR
  ...                                     (48 rows)
$ echo $?
1
```

48 controls, and exactly one line worth acting on. Drift is printed before the
matrix and the two reasons for `INDETERMINATE` are kept apart — a context that
never scanned is a different fact from a control that scanned and produced no
evidence. The report is written before the non-zero exit, so a failed context
never costs you the evidence already gathered.

`go run .` emits the same report as deterministic JSON.

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

## What the real reports showed

Findings taken from the captured output, and what each one changed:

| Observation | Consequence | Test |
|---|---|---|
| `passed` is three-valued: 6 `irrelevant`, 1 `w/exceptions`, 9 plain | a cell must carry `subStatus` and `ResourceCounters`, not a status string | `TestRealReport_PassedIsThreeValued` |
| all 6 `irrelevant` passes examined **zero** resources | "passed" and "nothing to check" must not read alike | same |
| `clusterName` is `""` in every report | identity is not recoverable; the orchestrator carries the context through | `TestRealReport_CarriesNoClusterIdentity` |
| 47 of 48 controls identical across the fleet | drift must be surfaced separately or the one row that matters is buried | `TestRealFleet_SingleDriftIsQualifiedPass` |
| the single drift, C-0057, is `passed (w/exceptions)` -> `failed` | flattening `status` alone loses that the baseline pass was already qualified | same |

An unreachable context is recorded as `SCAN_ERROR` and the remaining contexts
still scan (`TestRealFleet_UnreachableContextDoesNotAbortFleet`). That path is
only reachable because `Kubescape.Scan` now returns an error for an
unreachable cluster instead of calling `logger.Fatal`
([kubescape/kubescape#2788](https://github.com/kubescape/kubescape/pull/2788)).

## Deliberate non-goals

Concurrency. SaaS aggregation. Changing existing single-cluster report
schemas. Cluster discovery. Importing Kubescape's Go types directly — the
loader here reads the report JSON, whereas a production implementation would
derive `ClusterScanResult` from the in-process `PostureReport` returned by
`ResultsHandler.GetResults()`.

## Open design questions (for maintainers)

1. Should `FleetReport` live in `core/pkg/fleet`, or alongside existing
   report types?
2. On an unreachable context: continue with a partial report and non-zero
   exit, or fail fast? (Spike implements continue-on-error.)
3. Should the first printer reuse existing result-printer infrastructure, or
   stay fleet-specific until the report shape stabilizes?
