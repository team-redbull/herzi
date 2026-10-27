# Controller implementation notes
These are implementation decisions moved out of the code.

- Reconcile: The explicit InfraEnv Get overrides the list result because creation or deletion can race with listing.
- deleteAfterSpec: Register object cleanup before manager startup so LIFO cleanup stops the manager before deleting objects and isolates subsequent specs.
- world: An accepted fake order becomes open runs in later sweeps; otherwise a fresh observation would legitimately order the same deficit again and misrepresent the real ports.
