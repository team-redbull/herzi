# Installer implementation notes
These are implementation decisions moved out of the code.

- New: Copy the supplied HTTP client and reject redirects to avoid replaying an additive POST or forwarding credentials to another endpoint.
- New: The default transport is a clone of http.DefaultTransport with keep-alives disabled. A POST is not idempotent and herzi never retries it, so a reused pooled connection that the router or a firewall had already dropped would fail the request at once (Go retries a failed reused connection only for idempotent requests, or when nothing was written). Each request opens a fresh connection, as curl does; the cost is one TLS handshake per order, which is rare.
- New: `CAFile` (env `HERZI_INSTALL_CA_FILE`) adds a PEM bundle to the system roots on the default transport only; an unreadable file or one without a certificate fails startup instead of silently trusting nothing extra. A supplied HTTPClient keeps its own TLS settings.
- New: A transport error is classified (timeout, dns, refused, reset, closed, unreachable, tls_*) and carried as pipeline.InstallError so logs and metrics show the cause; the bearer token, URL user info and query string never appear in it.
- New: An omitted timeout defaults to 30 seconds, below the default reconcile deadline; unreleased builds identify themselves as herzi/dev in the User-Agent.
- Install: Ignore body-close errors after reading the response because they cannot change the reported outcome.
- parseReport: Decode complete items independently to retain earlier slot reports when a body is interrupted; accept unknown fields such as workflow_id for response compatibility.
