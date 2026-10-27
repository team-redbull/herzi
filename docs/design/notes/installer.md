# Installer implementation notes
These are implementation decisions moved out of the code.

- New: Copy the supplied HTTP client and reject redirects to avoid replaying an additive POST or forwarding credentials to another endpoint.
- New: An omitted timeout defaults to 30 seconds, below the default reconcile deadline; unreleased builds identify themselves as herzi/dev in the User-Agent.
- Install: Ignore body-close errors after reading the response because they cannot change the reported outcome.
- parseReport: Decode complete items independently to retain earlier slot reports when a body is interrupted; accept unknown fields such as workflow_id for response compatibility.
