# Independent review, round 2: findings

Source: a second, independent Codex review of the whole tree (read-only, run after the author's own
hardening pass in `controller-review.md`). Verbatim; the fixes and their tests are tracked in the design
document (Part 3) and in `independent-review-round-2-fixes.md`.

I would not run this in production yet. The spending throttle still permits duplicate orders, and Describe-only reconstruction loses evidence needed by the probe rule.

I read the requested production code, design, plan, API files, and self-review, and skimmed test coverage. **No files were modified, no git write commands were used, and no tests were executed.** “VERIFIED” below means the code establishes the stated conditional failure path; it does not mean reproduced against live services.

1. **HIGH — A slow recheck bypasses the spending throttle and duplicates an order. VERIFIED.**

   References: [reconciler.go:189](/home/toto/code/herzi/internal/controller/reconciler.go:189), [reconciler.go:223](/home/toto/code/herzi/internal/controller/reconciler.go:223), [reconciler.go:381](/home/toto/code/herzi/internal/controller/reconciler.go:381), [sweeper.go:54](/home/toto/code/herzi/internal/temporalx/sweeper.go:54).

   Demand 10, assigned 10: target 12. A sweep finishes at `t=0`; POST orders two machines at `t=0.1`. A watch reconciliation at `t=9` receives that cached, pre-order sweep. Its uncached Kubernetes recheck takes two seconds. At `t=11`, `reserveAttempt` permits another order because ten seconds have elapsed since the previous attempt. Both decisions used zero open runs, so another two machines start: eventual supply **14 against target 12 and ceiling 13**.

   **Fix:** Tie spending eligibility to the sweep’s generation or observation timestamp. Invalidate the sweep after every attempted POST and require a subsequent observation before spending again. Checking elapsed time only at the eventual POST is insufficient. The self-review’s throttle test does not cover crossing expiry during recheck.

2. **HIGH — The ambiguous-outcome block can be lost precisely when the reconcile deadline expires. VERIFIED.**

   References: [reconciler.go:109](/home/toto/code/herzi/internal/controller/reconciler.go:109), [reconciler.go:231](/home/toto/code/herzi/internal/controller/reconciler.go:231), [reconciler.go:336](/home/toto/code/herzi/internal/controller/reconciler.go:336), [reconciler.go:340](/home/toto/code/herzi/internal/controller/reconciler.go:340).

   Suppose gathering consumes 40 seconds of the default 60-second budget. The POST consumes the remaining 20 seconds and returns ambiguous. The annotation PATCH receives the already-expired reconcile context and cannot persist the block—even with a healthy Kubernetes API.

   With a deficit of 15, the next reconciliation can see no workflows yet and order another 15. If the first request subsequently starts, there are **30 orders against a deficit of 15**, without the intended five-minute protection.

   The block also expires relative to **reconcile start**, rather than the ambiguous outcome. A valid ten-minute reconcile timeout allows a five-minute POST to consume almost the entire default block interval.

   **Fix:** Persist a conservative pending-order marker before sending the additive POST; leave it on uncertain outcomes. Measure the ambiguity wait from the outcome, and give outcome persistence a reserved, bounded budget. Preserve restart safety if persistence fails.

3. **HIGH — Valid configuration can make running slots invisible after restart. VERIFIED.**

   References: [validate.go:33](/home/toto/code/herzi/internal/config/validate.go:33), [sweeper.go:95](/home/toto/code/herzi/internal/temporalx/sweeper.go:95), [router.py:489](/home/toto/code/workflows/workflow_domains/server_lifecycle/router.py:489).

   Configuration accepts `maxSlots: 15`, although the router always allocates among 100 slots.

   Example: demand 20 gives target 23. Under the previous configuration, `maxOpenRunsPerPool: 30` permits 23 runs occupying slots 1–23. Restart with accepted settings `maxSlots: 15`, `maxOpenRunsPerPool: 15`. Slots 1–15 finish and produce 15 Agents; slots 16–23 remain running. The controller sees supply 15, orders eight replacements, and eventually has **31 machines against target 23**.

   **Fix:** Require `maxSlots == 100` for this API version, or discover the authoritative slot range through a validated API contract. Use `maxOpenRunsPerPool` exclusively for load tuning.

4. **HIGH — Slot reuse erases probe evidence and prevents reliable consecutive-failure counting. VERIFIED.**

   References: [sweeper.go:126](/home/toto/code/herzi/internal/temporalx/sweeper.go:126), [decide.go:49](/home/toto/code/herzi/internal/pipeline/decide.go:49), [decide.go:65](/home/toto/code/herzi/internal/pipeline/decide.go:65), [router.py:489](/home/toto/code/workflows/workflow_domains/server_lifecycle/router.py:489).

   Two failures follow from retaining only the latest execution per slot:

   - Demand six, seven existing Agents: one installation fails for capacity in slot 1. Twenty minutes later, its probe reuses slot 1, erasing the only visible failure. Demand then rises to ten. With seven Agents and one ten-second-old probe, the controller sees no capacity evidence and orders **four Full machines before DrawGrace**, despite no proof of recovery.
   - An empty zero-demand pool needs two machines. Both repeatedly fail with `BmcCredentialsMissingError`. The router reuses slots 1 and 2 every time, so `otherCount` never exceeds two. Even after **100 consecutive failures**, the controller continues Full batches instead of entering probe mode after three failures.

   [decide_test.go:116](/home/toto/code/herzi/internal/pipeline/decide_test.go:116) explicitly expects Full after failure evidence is overwritten; this encodes the defect.

   **Fix:** Retain restart-safe failure/probe state and deduplicate executions by run ID, or reconstruct enough prior executions to preserve the required evidence. Latest execution plus its close event is insufficient.

5. **MEDIUM — Failure-history errors silently disable capacity throttling. VERIFIED.**

   References: [sweeper.go:150](/home/toto/code/herzi/internal/temporalx/sweeper.go:150), [sweeper.go:194](/home/toto/code/herzi/internal/temporalx/sweeper.go:194), [decide.go:66](/home/toto/code/herzi/internal/pipeline/decide.go:66).

   Describe succeeds for two capacity-failed slots, but close-history reads fail through timeout or authorization. `failureType` returns `""` without setting `SweepResult.Err`. Both failures become “other”; two is below the fallback threshold.

   An empty default pool can consequently issue **two failed installations every two minutes**, instead of one every twenty minutes. Slot reuse keeps the apparent failure count at two indefinitely.

   **Fix:** Propagate an unknown failure classification for recent failed runs and block Full spending until it is resolved. Retrying later is useful, but the current reconciliation must not treat missing evidence as permission to spend.

6. **MEDIUM — Two minutes of workflow runtime does not prove a machine was found. VERIFIED.**

   References: [decide.go:89](/home/toto/code/herzi/internal/pipeline/decide.go:89), [install_server.py:284](/home/toto/code/workflows/workflow_domains/server_lifecycle/install_server.py:284), [install_server.py:587](/home/toto/code/workflows/workflow_domains/server_lifecycle/install_server.py:587), [install_server.py:618](/home/toto/code/workflows/workflow_domains/server_lifecycle/install_server.py:618).

   Before drawing hardware, the workflow resolves the segment and validates the InfraEnv. Its activity retry policy has no attempt limit. All three queues can have pollers while a dependency returns retryable errors.

   Demand six, target eight: after capacity failures, one probe starts but remains in `resolving-vlan` or retrying `acquire_servers`. After two minutes, it becomes “proof,” and the controller orders **seven more**, although no server was acquired.

   **Fix:** Expose durable evidence that acquisition/reservation actually succeeded, and apply the after-failure proof rule to that evidence. Conservatively accepting completion is another option. Workflow age alone cannot establish the design’s premise.

7. **MEDIUM — Accepted timing settings can bypass the twenty-minute probe interval. VERIFIED.**

   References: [validate.go:84](/home/toto/code/herzi/internal/config/validate.go:84), [decide.go:73](/home/toto/code/herzi/internal/pipeline/decide.go:73).

   `failureMemory: 1m` with `probeInterval: 20m` passes validation. At the normal two-minute reconciliation, the latest capacity failure has already expired. The controller returns Full rather than waiting twenty minutes: an empty default pool can request **60 installations/hour instead of three probes/hour**.

   **Fix:** Enforce the timing relationships required by the probe guarantee, including `failureMemory >= probeInterval` with an appropriate scheduling margin. Test the interactions, not only each duration’s positivity.

8. **HIGH — One malformed unrelated object can indefinitely block the entire hub. VERIFIED.**

   References: [gather.go:43](/home/toto/code/herzi/internal/controller/gather.go:43), [gather.go:60](/home/toto/code/herzi/internal/controller/gather.go:60), [gather.go:77](/home/toto/code/herzi/internal/controller/gather.go:77), [validate.go:38](/home/toto/code/herzi/internal/pipeline/validate.go:38).

   Every parse error has `ReadError.Pool == ""`. The initial gather scans all Agents in the namespace, all InfraEnvs there, and all NodePools.

   If one legacy/malformed Agent belonging to unmanaged pool B contains an unparsable timestamp, **all 20 healthy managed pools** receive a hub-wide read error and stop growing until that object is repaired. The same applies to a malformed unrelated NodePool or InfraEnv.

   This contradicts the design’s pool-isolation requirement. The self-review acknowledges the behavior but incorrectly treats the implementation mechanics as sufficient justification.

   **Fix:** Extract identity and attribution before parsing optional fields; scope attributable errors to their pool. Unrelated or unattributable malformed objects should produce warnings where they cannot affect the current pool’s accounting. Preserve hub-wide blocking for genuinely hub-wide read failures.

9. **MEDIUM — An old hung run can suppress recovery probes for six hours. VERIFIED.**

   References: [decide.go:34](/home/toto/code/herzi/internal/pipeline/decide.go:34), [decide.go:73](/home/toto/code/herzi/internal/pipeline/decide.go:73), [install_server.py:300](/home/toto/code/workflows/workflow_domains/server_lifecycle/install_server.py:300).

   One run starts at 10:00:00; another closes with a capacity failure at 10:00:20. The earlier run hangs in indefinitely retried activity work. Hardware becomes available at 10:30, and all queues have pollers.

   The older run cannot prove recovery because it started before the failure. Nevertheless, `running > 0` suppresses every new probe, even with **14 free positions under the default open-run cap**. Recovery waits until failure memory expires around 16:00.

   This is bounded by the default six-hour memory, so I would **not** call it an infinite default-config deadlock. It still breaks the promised twenty-minute recovery opportunity.

   **Fix:** Distinguish a current post-failure probe from older in-flight work. Permit one scheduled probe while preserving the overall open-run cap.

10. **MEDIUM — A semantic error in one pool rejects configuration changes for every pool. VERIFIED.**

    References: [load.go:154](/home/toto/code/herzi/internal/config/load.go:154), [validate.go:42](/home/toto/code/herzi/internal/config/validate.go:42), [store.go:48](/home/toto/code/herzi/internal/config/store.go:48), [reconciler.go:239](/home/toto/code/herzi/internal/controller/reconciler.go:239).

    A valid YAML update pauses pool A and accidentally sets pool B’s `minBand: 0`. The entire update is rejected, so **A continues ordering** under its previous configuration. At startup, one such pool error prevents the whole controller from starting.

    The design distinguishes malformed configuration—which retains last-good—from merged pool validation errors—which block only that pool. The controller’s `PoolErrors` handling cannot provide that behavior because `Load` rejects the configuration first.

    **Fix:** Separate document/hub validation from per-pool semantic validation. Publish valid pool changes and explicitly block invalid pools, as specified.

11. **LOW — `stuckAfter` is an ineffective configuration knob. VERIFIED.**

    References: [reconciler.go:355](/home/toto/code/herzi/internal/controller/reconciler.go:355), [metrics.go:95](/home/toto/code/herzi/internal/metrics/metrics.go:95), [herzi-alerts.yaml:55](/home/toto/code/herzi/config/prometheus/herzi-alerts.yaml:55).

    Setting `stuckAfter: 30m` changes neither decisions nor alert thresholds. A 45-minute run still does not alert; the shipped rule remains hard-coded to 7,200 seconds.

    **Fix:** Publish/use the effective threshold in alerting, or remove the knob and document a fixed operational threshold. The import-only scaffolding in `pipeline/stages.go` is also removable dead code.

**Hand recomputation against the real functions:** [Decide](/home/toto/code/herzi/internal/pipeline/decide.go:18) uses integer half-up rounding, and [Plan](/home/toto/code/herzi/internal/pipeline/plan.go:7) applies headroom and the API cap correctly.

| Inputs, using defaults unless stated | Calculation | Actual decision |
|---|---|---|
| Demand 10; assigned 10, claimable 2 | low 2, high 3; target 12, ceiling 13; supply 12 | Hold `"inside band"` |
| Demand 16; assigned 12 | low `round(2.4)=2`; high `round(4.8)=5`; target 18; need 6 | Grow 6 Full |
| Demand 50; empty | low `round(7.5)=8`; high 15; target 58 | Grow 58 Full; Plan 15 |
| Demand 4; assigned 4, claimable 3, settling 16 | target 6, ceiling 7; supply 23 | Hold; settling suppresses removal |
| Demand 12; same supply | low 2, high 4; target 14, ceiling 16; supply 23 | Hold; settling suppresses removal |
| Demand 20; assigned 18, claimable 3 | low 3, high 6; target 23; supply 21 | Grow 2 Full |
| Demand 6; assigned 6, claimable 4 | target 8, ceiling 9; total 10; surplus `min(10−8,4)=2` | WouldRemove 2 |
| Demand 6; one three-minute-old run started after a 21-minute-old capacity failure | target 8; supply 1; valid age-based proof | Grow 7 Full |

The design’s custom-buffer demand-20 table explicitly specifies low/high **2/4**; its Grow-1 result is therefore correct and differs intentionally from defaults. One documentation arithmetic error remains at [herzi-design.md:85](/home/toto/code/herzi/docs/design/herzi-design.md:85): ten completions from an initial fifteen-run wave leave **10 Agents + 5 open**, not “25 + 5.” Ordering another ten is still correct.

For Temporal, fixed-ID Describe avoids visibility-list lag, and pinning the history read to the described run ID is correct. Failure extraction follows ActivityFailure causes to ApplicationFailure and handles the actual direct `ApplicationError` types. Python raises `ServerNotAvailableError`/`ServerReservedError` for homogeneous capacity rejection; mixed rejection reasons become `NoBondableInterfacesError`, correctly entering Go’s “other” path. The defects are lost prior-run evidence and silently unknown history, not misspelled error types. Retention loss returning the pool to Full is already documented as accepted.

I found no demonstrated production data race or detached RPC goroutine leak. Channel closure synchronizes cached results; shared maps/LRU state are locked; normal four-pool execution permits approximately **40 simultaneous sweep RPCs**. The three default queue names match Python, so there is no default nonexistent-queue blocker. External calls have deadlines, but the annotation path mishandles deadline exhaustion.

I found no TLS-verification bypass or production log/metric secret leak. Redirects are disabled, bearer-token CR/LF is rejected, response bodies are bounded to 1 MiB, and normal request size is small. Response reports do not determine supply; Temporal does. These checks do not resolve the ordering defects above.

Coverage is extensive for isolated functions, but the reviewed tests miss the delayed-recheck expiry race and realistic repeated slot reuse; some explicitly bless evidence loss. I did not claim live lease-handoff, restart, or Temporal integration validation.

Model/effort: session instructions identify this as Codex based on GPT-6; the exact model variant and effort setting are not exposed.

**The five changes I would insist on before production:**

1. Bind every order to a fresh sweep generation and prevent reuse of pre-order observations.
2. Make pending/ambiguous-order protection durable before spending and safe under deadline exhaustion.
3. Make probe evidence restart-safe, preserve failure streaks, require acquisition evidence, and prevent older hung runs from starving probes.
4. Enforce the API’s full slot range and the probe timing invariants.
5. Isolate object-parse and semantic configuration failures to the affected pool.

