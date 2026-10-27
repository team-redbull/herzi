# Pipeline implementation notes
These are implementation decisions moved out of the code.

- Attribute: The result has no hub-level container, so each pool carries the same hub warnings; consumers must deduplicate them when reporting the hub.
- Attribute: Input slices are not sorted or changed in place, allowing a gathered snapshot to be reused safely.
- RunSweeper, QueueChecker, Installer: I/O interfaces live beside the pipeline types they return so adapters and controller test fakes share the same contracts.
- Hold: Reason strings are stable because callers use them to report decisions.
