# Implementation decision notes

These notes preserve decisions and rationale moved out of code.

- [chart.md](chart.md): Chart packaging, deployment, security, configuration and monitoring decisions.
- [cmd.md](cmd.md): Manager build metadata, environment, metrics protocol and sweep timing.
- [config.md](config.md): Strict loading, immutable snapshots, updates and file watching.
- [controller.md](controller.md): Reconciliation races, integration cleanup and fake-order observations.
- [ids.md](ids.md): Workflow ID separators and collision avoidance.
- [installer.md](installer.md): HTTP client isolation, redirects, timeouts and partial responses.
- [kube.md](kube.md): Unstructured API views, parsing errors and Agent transition timestamps.
- [metrics.md](metrics.md): Collector registration, stale series and conflict detection.
- [pipeline.md](pipeline.md): Pure-stage input ownership, shared ports and stable reasons.
- [temporalx.md](temporalx.md): Observation callbacks, clocks, sweep concurrency, history and queue reads.

When a change introduces an implementation decision worth preserving, add or
update `docs/design/notes/<package>.md` (use `cmd.md` for manager wiring and
`chart.md` for chart decisions). State the decision and why it matters, link the
relevant design or operations contract, and add a one-line entry to this index
for each new file. Keep necessary contracts, markers, package lines, invariants
with a one-line why, and nolint reasons in code; keep extended rationale here.
