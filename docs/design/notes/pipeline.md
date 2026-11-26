# Pipeline implementation notes
These are implementation decisions moved out of the code.

- Attribute: The result has no hub-level container, so each pool carries the same hub warnings; consumers must deduplicate them when reporting the hub.
- Attribute: Input slices are not sorted or changed in place, allowing a gathered snapshot to be reused safely.
- RunSweeper, QueueChecker, Installer: I/O interfaces live beside the pipeline types they return so adapters and controller test fakes share the same contracts.
- Hold: Reason strings are stable because callers use them to report decisions.
- Retiring: A released Agent counts as retiring when it carries the `redbull.io/ever-installed` key with any value, the same test a NodePool's `DoesNotExist` exclusion applies. It is checked after Bound and Deleting, so a labelled bound Agent is still Assigned.
- Retirement, DecideRemove: Two pure functions so the controller observes the hub only for candidates. DecideRemove is defensive: it re-applies Retirement, so a fresh uncached Agent can be decided with the same call.
- Unguarded: A NodePool guards an Agent when its full selector does not match the Agent's labels, which is exactly when CAPI cannot pick it. A NodePool without a selector matches every Agent, as CAPI treats it. Paused and deleting NodePools count, because they can resume.
- Wait reasons: Stable strings; they are logged and, through the state, drive `herzi_retiring_agents`.
