# Controller implementation notes
These are implementation decisions moved out of the code.

- Reconcile: The explicit InfraEnv Get overrides the list result because creation or deletion can race with listing.
- deleteAfterSpec: Register object cleanup before manager startup so LIFO cleanup stops the manager before deleting objects and isolates subsequent specs.
- world: An accepted fake order becomes open runs in later sweeps; otherwise a fresh observation would legitimately order the same deficit again and misrepresent the real ports.
- Retirer: It is a second controller keyed by Agent because the pool reconciler returns early for ignored or excluded InfraEnvs and never sees an Agent without a pool label, while retirement covers every Agent in the namespace. It shares the pool reconciler's client, reader, config snapshot, poller cache, metrics and clock through `NewRetirer`.
- Retirer labelling: The label is written on bind, before any gate, because a bind and release while the label is missing return the server to the pool forever. CAPI picks Agents by selector only while `AgentMachine.Status.AgentRef` is empty, so the label cannot disturb an install.
- Retirer removal: The cap check and the POST run under one mutex so concurrent workers cannot overshoot `maxOpenRemovals`; the open count describes the remove ids of every retiring Agent through the 10-second cache. The uncached re-read runs `DecideRemove` again and proceeds only with the same server name.
- Retirer outcomes: No pre-POST marker is written. The remove id is fixed per server, so a duplicate POST is a 409 and a re-run after completion finds nothing; an ambiguous outcome is resolved by the next Describe. A 4xx is held in memory for `removeRetryAfter` so a broken request is not resent every interval.
- Retirer states: `paused` and `dry_run` are separate from `waiting` so `HerziRetirementSlow` does not fire during a planned rollout; `herzi_oldest_retiring_seconds` restarts when an Agent moves into `waiting` or `removing`.
- Retirer and uninstall-server-from-hc: That workflow unbinds the server and then polls the same Agent until it is a known-unbound, approved Agent with its BareMetalHost, failing if the Agent disappears. The retirer therefore waits while `uninstall-server-from-hc-<server>` is Running and removes the server after it completes; a failed uninstall does not hold removal.
- mergePatch: Agent labels and InfraEnv annotations share one merge-patch helper with an independent ten-second context, so a canceled reconcile cannot drop a write it already decided on.
- removerFake: A successful fake removal registers a Running remove run, as the route does; otherwise the next pass would correctly ask again.
