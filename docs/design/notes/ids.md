# Workflow ID implementation notes
These are implementation decisions moved out of the code.

- PoolSlot: Underscores separate the hub, pool and slot; Kubernetes names cannot contain underscores, so distinct valid tuples cannot collide even when names contain hyphens.
