# Metrics implementation notes
These are implementation decisions moved out of the code.

- Metrics.New: Collectors belong to the caller's registry, and duplicate registrations reuse existing collectors. The constructor retains registration conflicts for Controller.New to reject because its API has no error return.
- Metrics.ObservePool: Clear stale reason and failure-class series on each observation so a recovered pool does not retain an earlier diagnosis.
- TestRegistrationConflicts: Register an unobserved vector explicitly because Gather omits vectors with no observed labels and cannot detect that conflict alone.
