# Temporal implementation notes
These are implementation decisions moved out of the code.

- Options.Observe: The callback receives a pool name for sweeps and a queue name for queue reads; it must return promptly and support concurrent calls so instrumentation does not delay reads.
- Options.Now: The injected clock governs sweep-cache age only; RPC deadlines and duration measurements use real time to retain actual timeout bounds.
- Sweeper: Concurrent calls for the same pool share an in-flight sweep, and returned slices are copies so callers cannot mutate cached results.
- Sweeper.Sweep: Wait for every RPC worker before returning so no detached worker outlives the sweep; discard canceled observations so they cannot poison the next reconcile.
- Sweeper.failureType: History errors leave the failure type unknown and are retried after the pool TTL rather than cached permanently, allowing recovery without assuming success.
- Queues.Pollers: Use plain DescribeTaskQueue without task reachability; preserve successful counts from other queues when one read fails so diagnostics retain available information.
