# Command implementation notes
These are implementation decisions moved out of the code.

- version / commit: Build-time -ldflags assignments to main.version and main.commit identify the binary and its source revision.
- run: Credential and TLS environment variables are documented in [operating guidance](../../ops/operating.md); client-go also honors KUBECONFIG and the standard in-cluster service environment, preserving local and in-cluster authentication.
- metricsOptions: HTTP/2 is opt-in to avoid Stream Cancellation and Rapid Reset vulnerabilities (GHSA-qppj-fm5r-hxr3 and GHSA-4374-p667-p6c8).
- run: The sweeper's ten-second cache TTL matches the controller's spending throttle so retry scheduling and observation reuse stay aligned.
