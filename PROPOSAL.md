# Native Multi-Cluster Fleet Posture Aggregation — LFX Mentorship 2026 Term 3

**Madhav Sharma** · GitHub [@madhav-sharma0201](https://github.com/madhav-sharma0201) · madhavsharma2023@gmail.com
Proposal: [cncf/mentoring#1990](https://github.com/cncf/mentoring/issues/1990)

---

> **Note.** This is my application proposal for the LFX Mentorship 2026 Term 3
> project *Native Multi-Cluster Fleet Posture Aggregation*
> ([cncf/mentoring#1990](https://github.com/cncf/mentoring/issues/1990)). It is
> published here so it can be linked from the LFX application form, which has
> no field for a document this size. The code in this repository is the spike
> the proposal refers to.

---


## The argument

Fleet scanning is not a shell loop wrapped in a new subcommand. Running
`kubescape scan --kube-context` twice already works; it produces two
disconnected reports nobody can reason about together. The engineering problem
is what the loop leaves undefined: per-context state isolation, deterministic
aggregation across clusters that may not share a control set, and honest
handling of a cluster that cannot be reached.

The last of those is not hypothetical, and it is where I started. Until last
week, an unreachable cluster did not return an error from `Kubescape.Scan` — it
called `logger.Fatal`, which is `os.Exit(1)`, from inside a reusable library
function. A sequential orchestrator that hit a bad context would not have
recorded a per-cluster error; the process would have died mid-fleet. I found
that, fixed it, and it is merged
([#2788](https://github.com/kubescape/kubescape/pull/2788)).

That is the shape of my case for this project: I have already removed
prerequisites this design depends on, upstream, under review, before the term
starts.

---

## What I have verified, with evidence

### Merged contributions

| PR | Size | What it proves |
|---|---|---|
| [#2788](https://github.com/kubescape/kubescape/pull/2788) — `Scan` returns cluster-connection failures instead of terminating | +50/−3 | The failure boundary the fleet orchestrator sits on. Was the only `logger.Fatal` in non-test `core/core`. |
| [k8s-interface#160](https://github.com/kubescape/k8s-interface/pull/160) — refresh API discovery for a newly initialized live client | +145/−15 | One of the two process-global blockers to holding a second cluster's client. Discovery for cluster B no longer resolves through cluster A's `resourceGroupMapping`. |
| [#2898](https://github.com/kubescape/kubescape/pull/2898) — label the report with the context the scan actually used | +109/−1 | Multi-cluster attribution. #2841 moved scan attribution onto the selected kubeconfig; #2866 merged 22 minutes later reading the process-global instead, so `--kubeconfig` produced a report labelled with the ambient cluster. |
| [#2761](https://github.com/kubescape/kubescape/pull/2761) — `GetPortForwardLocalhost` reports the bound port | +167/−3 | client-go lifecycle. With `DEFAULT_PORT_FORWARDER_PORT=0` the operator scan POSTed to `localhost:0`. |
| [#2810](https://github.com/kubescape/kubescape/pull/2810) — expose `ErrClusterConnection` as a sentinel | +9/−4 | An embedder can branch on "cluster unreachable" via `errors.Is` rather than string-matching — exactly what a fleet orchestrator needs to classify a context as unreachable rather than merely failed. Came from @matthyx's review of #2788. |
| [#2783](https://github.com/kubescape/kubescape/pull/2783) — repair `NewOPASessionObj` call broken by merge | +1/−1 | Found by running the suite across the tree; master's tests did not compile. |
| [#2785](https://github.com/kubescape/kubescape/pull/2785) — `t.Setenv` so env vars are restored between runs | +4/−4 | Test isolation: `-count=2` failed because a sibling test leaked an env var. |

All seven merged within four days, each reviewed and reproduced locally by
@matthyx before merge.

Six of the seven are the same problem seen from different angles: **state that
outlives the single invocation it was written for.** Discovery cached
process-wide so a second client inherits the first cluster's resources. A report
labelled from a process-global instead of from the scan's own context. A library
owning the process lifecycle by calling `os.Exit`. An environment variable
surviving between test runs. That is the exact class of bug a sequential fleet
orchestrator is made of — running a single-cluster path repeatedly without
letting anything leak between runs — and it is why I went after these
particular issues rather than whatever was open.

Two of them — #2783 and #2898 — are semantic merge conflicts: pairs of PRs each
correct alone and wrong together, found by running the suite across the tree
rather than by reading an issue.

### Issues filed

[#2787](https://github.com/kubescape/kubescape/issues/2787),
[#2784](https://github.com/kubescape/kubescape/issues/2784) and
[k8s-interface#159](https://github.com/kubescape/k8s-interface/issues/159) —
all three closed by my own PRs — plus
[#2856](https://github.com/kubescape/kubescape/issues/2856), the report-identity
trace below, which another contributor fixed in #2866.

### Reviews

- [#2892](https://github.com/kubescape/kubescape/pull/2892) — a proposed fix
  described as closing an unauthenticated arbitrary-Secret-read via
  `POST /v1/scan`. I checked the branch out and could not reproduce the premise:
  the scan object is added to `AllResources` (`k8sresources.go:138`), and
  `updateResults` redacts every Secret's `data` and `stringData` unconditionally
  before results are built, on the same path the httphandler uses. I raised it
  as a question with the evidence rather than as an assertion. @matthyx
  confirmed it — *"As verified, `removeData()` (`removeSecretData()`) redacts
  `data` and `stringData`"* — and made rewriting the security framing a
  condition of merge. The change shipped as defence in depth instead, which is
  the claim that actually holds.
- [#2689](https://github.com/kubescape/kubescape/pull/2689) — mutation-tested
  both fixes and found `statusLabel[status]` is an unchecked map lookup of the
  same shape as the bug being fixed.

### Spike

[github.com/madhav-sharma0201/kubescape-fleet-spike](https://github.com/madhav-sharma0201/kubescape-fleet-spike)
— 22 tests, race- and shuffle-clean. Deliberately **not** proposed as a PR: the
feature is the mentorship, and @matthyx asked candidates not to start the
project work before selection. It runs against real
`kubescape scan --format json` output captured from three kind clusters, not
invented structs.

---

## What running the tool actually taught me

These three findings came from reading 850KB of real output from three kind
clusters, and each one changed the design rather than confirming it.

### 1. `passed` is three-valued

From `kind-fleet-a`, 48 controls:

| Kind | Count | Meaning |
|---|---|---|
| `passed`, `subStatus: irrelevant` | 6 | **zero** resources examined — vacuous |
| `passed`, `subStatus: w/exceptions` | 1 | passing because an exception was applied |
| `passed`, no subStatus | 9 | genuine |

All six `irrelevant` passes have `ResourceCounters` summing to zero. Across a
fleet, `PASS → PASS` can therefore mean "77 resources checked in both" or
"nothing to check in either."

**Design consequence:** the matrix cell must carry `subStatus` and
`ResourceCounters`, not a status string. My spike's `ControlOutcome` does, and
`Vacuous()` distinguishes them.

### 2. The report carried no cluster identity, structurally

`clusterName` was `""` in all three of my reports, with and without
`--keep-local`; `metadata.clusterMetadata` was `{}`. That was not a property of
my fixtures. I traced it and filed
[#2856](https://github.com/kubescape/kubescape/issues/2856):

- `OPASessionObj.Report` is initialized as an empty `PostureReport`
  (`core/cautils/datastructures.go:85`).
- Nothing in the tree ever assigned `Report.ClusterName` or
  `Report.CustomerGUID`, and `git log -S` suggested nothing ever had.
- `FinalizeResults` copied those zero values straight into the emitted report.

The name was known during the scan — the submit path derives it from
`tenantConfig.GetContextName()` (`reporteventreceiver.go:95`) — it simply never
reached the report written to disk. Same class as
[#2325](https://github.com/kubescape/kubescape/issues/2325), where
`generationTime` emitted the Go zero value and was fixed by populating it in
that same function.

It was fixed in #2866. That fix read the process-global context name, though,
and #2841 had landed 22 minutes earlier moving scan attribution onto the context
the scan actually selected — so a `--kubeconfig` or `--kube-context` selection
yielded a report labelled with the ambient cluster instead. I found that,
reproduced it with a failing test, and
[#2898](https://github.com/kubescape/kubescape/pull/2898) is merged.

**Design consequence:** the orchestrator must carry the requested kube-context
through as the column key rather than reading identity back out of the report.
The field being populated does not change that — as #2898 shows, a report's
self-reported cluster is only as trustworthy as whichever global the writer
happened to read. For a fleet run, the authoritative identity is the context the
orchestrator asked for.

### 3. Drift is a needle in a haystack, and the needle is qualified

Across three clusters differing by one privileged pod, **47 of 48 controls are
identical**. The single difference:

```
C-0057:  kind-fleet-a = passed (w/exceptions)  ->  kind-fleet-b = failed
         kind-fleet-a vs kind-fleet-c          ->  no drift at all
```

**Design consequence:** drift must be surfaced separately from the matrix or
the one row that matters is buried. And note the baseline side was not a plain
pass — a drift report that flattens `status` alone reports `PASS → FAIL` and
silently loses that the baseline was already passing only by exception.

### Where I am deliberately not overclaiming

- `skipped` was **uniform** across all three clusters (C-0069, C-0070,
  operator-only). The concern that `skipped` conflates causes is visible in the
  schema; I did **not** observe it in my data.
- The `NewPolicyHandler` race I reproduced is **not reachable** through current
  callers — `watchForScan` is single-consumer, started once at
  `requestshandler.go:59`. I report it as a latent hazard with that limit
  stated.

---

## Why sequential, in this codebase specifically

The proposal already commits to sequential execution. Having traced the path, I
think that is right, and I can name the reasons rather than restate them:

- `cmd/root.go:69` calls `k8sinterface.SetClusterContextName(rootInfo.KubeContext)`
  once in `PersistentPreRun`. Context selection is process-global, set before
  any scan runs.
- Several `sync.Once` guards on the scan path are process-lifetime by design:
  `celEvaluatorOnce` and `opaRegisterOnce` (`opaprocessor/processorhandler.go`),
  `vapCatalogOnce` and `controlConfigOnce` (`opaprocessor/cel/loader.go`),
  `initOnce` (`core/metrics`), `gitTransportOnce` (`cautils/remotegitutils.go`).

One layer is **already handled**, and it is worth being precise about which.
`NewPolicyHandler` (`core/pkg/policyhandler/handlepullpolicies.go:51`) now
re-keys `policyHandlerInstance` on `clusterName` and closes the stale instance
before replacing it, so exception policies no longer leak between clusters —
that was [#2742](https://github.com/kubescape/kubescape/pull/2742), and it is
the concrete example of what a correct context-switch fix looks like in this
codebase. I read it while reviewing, not to duplicate it.

The blockers that remain sit one level down, in `k8s-interface`, where the
client actually lives:

- [#158](https://github.com/kubescape/k8s-interface/issues/158) (opened by
  @shreyashsri79) — `SetClusterContextName` updates the name but does not
  re-point `K8SConfig`/`clientConfigAPI`, so a second context reports its own
  name and the first context's server.
- [#159](https://github.com/kubescape/k8s-interface/issues/159), which I found
  and reproduced — `InitializeMapResources` returns early whenever discovery is
  already loaded, so a client built for cluster B keeps resolving resources
  through cluster A's `resourceGroupMapping` and `resourceNamesapcedScope`.

These are independent: the discovery layer has no caller-side workaround at all,
because those globals are unexported and never reset, which @shreyashsri79
confirmed on the thread after I raised it.

[#2004](https://github.com/kubescape/kubescape/issues/2004) — cited in the
project description — is specifically the `PolicyHandler` singleton and its
race conditions. Given #2742, the exception-leakage half of it is addressed;
the concurrency half is not, and is correctly out of scope for this term.
Sequential execution sidesteps all of the above by holding exactly one live
client at a time, which is why I think the proposal's choice is right rather
than merely conservative.

---

## Proposed design

```
kubescape scan fleet --contexts a,b,c --baseline a

  validate contexts and baseline      before any cluster is contacted
  sequential orchestrator             behind a ContextScanner interface
  per context:
      re-point the global client      one live client at a time
      run the existing scan path      unmodified
      normalize the PostureReport     via ResultsHandler.GetResults()
  aggregate                           control union -> matrix -> drift
  emit FleetReport                    deterministic JSON
  exit non-zero if any context failed after the report is written
```

**Status model.** Five states, because collapsing them is the failure mode a
posture tool exists to prevent: `PASS`, `FAIL`, `NOT_EVALUATED` (present but
skipped), `MISSING` (absent from this cluster's control set), `SCAN_ERROR`
(the context did not scan). A missing control is not a passing control; an
unreachable cluster is not a compliant cluster.

**Failure semantics.** Continue on error, record a per-context error as
first-class report data, mark that column `SCAN_ERROR` in every row, and exit
non-zero after writing the report. A partial fleet report is useful; a fleet
report that silently omits a cluster is dangerous.

**Determinism.** Columns follow requested context order; rows sort by control
ID. Two runs over the same evidence produce byte-identical JSON. Tested.

### Explicit non-goals

Concurrency. SaaS/platform aggregation. Changes to the existing single-cluster
report schema, printers, or public API. Cluster discovery. Remediation. A
controller or CRDs.

---

## Deliverables (per the proposal)

1. `cmd/scan/fleet.go` — `scan fleet` with `--contexts` and `--baseline`,
   reusing existing scan flags.
2. `core/pkg/fleet/orchestrator.go` — sequential orchestrator running a
   complete, unmodified scan per context.
3. `FleetReport` with a control × cluster matrix plus baseline drift detection.
4. At least one printer (JSON first, then table).
5. Tests for multi-context orchestration, missing/unreachable contexts, and
   drift against a baseline.

---

## Testing strategy

The proposal lists tests for multi-context orchestration, missing or
unreachable contexts, and drift as a deliverable. I have already written that
suite once, against a fake scanner and against real captured reports, so this
section describes what I would carry over rather than what I hope to manage.

**Test the failure cases, not the happy path.** Two clusters that both scan
cleanly is the easy case and proves almost nothing. The cases that decide
whether the feature is trustworthy are:

| Case | Assertion |
|---|---|
| Context unreachable mid-fleet | Recorded as a per-context error; remaining contexts still scan; that column is `SCAN_ERROR` in every row; exit non-zero **after** the report is written |
| Control absent from one cluster | `MISSING`, never `PASS` — a framework or version mismatch is not compliance |
| Control present but skipped | `NOT_EVALUATED`, distinct from both `PASS` and `MISSING` |
| Baseline itself failed to scan | Explicit "baseline unavailable" error, not a panic and not a silent all-clear |
| Baseline not supplied | Validation error **before** any cluster is contacted |
| Identical clusters | Zero drift findings — no false positives |
| Context order changed | Matrix columns follow requested order, not sorted order |
| Same evidence scanned twice | Byte-identical JSON |

**Three test seams, in increasing cost.** The orchestrator depends only on a
`ContextScanner` interface, so most of this needs no cluster at all:

1. *Fake scanner* — synthetic edge cases: missing controls, per-context
   failures, ordering, drift classification. Fast, deterministic, runs in CI.
2. *Replayed real reports* — the aggregation runs against actual
   `kubescape scan --format json` output captured from three kind contexts, so
   the status vocabulary and the shape of the data are the tool's own rather
   than my guess at them. This is how I found that `passed` is three-valued.
3. *Live kind fixture* — three contexts plus one deliberately unreachable, for
   the integration suite in week 11.

**One thing I would want to get right early:** a test asserting a control is
`PASS` in both clusters must also assert *why*. Given the `irrelevant` finding
above, `PASS`/`PASS` over zero resources and `PASS`/`PASS` over 77 resources
are different facts, and a test that only compares status strings would pass
for both.

---

## Twelve-week plan

Weeks 2–4 and 7–9 rebuild, against real types and under review, what the spike
already covers with 22 tests. I am not proposing to discover this design during
the term; I am proposing to land it properly.

| Week | Work | Acceptance criterion |
|---|---|---|
| 1 | Confirm architecture, semantics and package ownership; settle the three open questions below | Approved design note |
| 2 | `FleetReport`, `ControlOutcome`, five-state status model — carrying `subStatus` and `ResourceCounters`, per finding 1 | Unit-tested data model |
| 3 | `ContextScanner` interface + fake runner (the seam the spike is built on) | Orchestrator testable without clusters |
| 4 | Sequential orchestration, one live client at a time | Ordered scans, per-context results, no shared mutable state between contexts |
| 5 | `scan fleet` with `--contexts` and `--baseline`, reusing existing scan flags | Validation runs before any cluster is contacted |
| 6 | Failure handling: unreachable, missing, baseline-unavailable | The failure table above passes; exit code is non-zero only after the report is written |
| 7 | Control union and matrix, keyed on the requested context (per finding 2) | Deterministic matrix; column order follows input |
| 8 | Drift classification against the baseline | Regression / improvement / indeterminate; qualified passes not flattened (finding 3) |
| 9 | JSON printer | Byte-identical output across runs; golden fixtures |
| 10 | Table printer | Human-readable matrix with drift surfaced separately from the full grid |
| 11 | kind multi-context integration suite | Three contexts plus one unreachable, green in CI |
| 12 | Docs, examples, compatibility review | Review-ready final PR set |

Shipped as five incremental PRs — types → orchestrator → CLI → matrix/drift →
printers and docs — not one large feature PR. That sequencing is the same one I
have been using upstream: small, independently reviewable, each with a test that
fails without it.

---

## Open questions I would want settled in week 1

1. Should `FleetReport` live in `core/pkg/fleet`, or alongside the existing
   report types?
2. On an unreachable context: continue with a partial report and a non-zero
   exit, or fail fast? My leaning is continue-with-explicit-errors.
3. Should the first fleet printer reuse the existing result-printer
   infrastructure, or stay separate until the report shape settles?

The proposal names two of these itself (module layout, and where OSS
aggregation stops versus platform aggregation). I have a leaning on each and
would rather match your preference than defend mine.

---

## Risks

| Risk | Mitigation |
|---|---|
| `k8s-interface` context-switching (#158, #159) not fixed in time | Orchestrator is built behind `ContextScanner`; aggregation is testable and shippable while the client layer is fixed in parallel |
| Report shape churn during the term | JSON printer last, golden fixtures only once the shape settles |
| Scope creep toward concurrency | Explicit non-goal; gated on #2004 |
| Drift semantics disputed | Classification is a pure function over two statuses, with `INDETERMINATE` as the honest default |

---

## Working style

I open small PRs with a failing test first, and I do not mark one ready before
CI is green. When @matthyx left three non-blocking follow-ups on #2788, I
implemented all three the same day ([#2810](https://github.com/kubescape/kubescape/pull/2810)).
When I found that k8s-interface#158 had a second, separate root cause, I filed
mine separately and said explicitly on the thread that I was not trying to take
theirs over; @shreyashsri79 confirmed the correction and we agreed the two
should stay independent.

I am in IST (UTC+5:30), which overlaps the CET working day through my
afternoon, and I can commit 25 hours a week for the term.

I have kept all fleet-specific work out of public PRs, in line with your
request that candidates not start the project before a mentee is chosen. The
spike exists so I can show my reasoning without pre-empting that decision.
