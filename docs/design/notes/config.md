# Config implementation notes
These are implementation decisions moved out of the code.

- durationText: Preserve YAML scalar types until conversion so numeric values cannot become duration strings; defer parsing to include the complete field path in errors.
- Validate: Canonicalization belongs to Load; callers constructing Config directly must supply canonical values before validation.
- NewStore: Copy the initial pool map and override pointers to isolate later caller edits. Valid is initially true, before any update attempt fails.
- Snapshot: Published pool maps and override pointers are immutable, and old snapshots remain usable after updates; ForPool returns value copies.
- Update: Serialize concurrent updates, including unchanged-byte successes, and own the comparison bytes so caller mutations cannot corrupt the no-op cache.
- FileWatcher: Polling kubelet-updated volume files avoids ConfigMap RBAC and a second informer. Start implements the controller-runtime Runnable contract and blocks until cancellation; each replica watches independently so its configuration stays fresh.
- Config: JSON tags specify field names; duration strings require Load's custom conversion because time.Duration does not decode them directly.
