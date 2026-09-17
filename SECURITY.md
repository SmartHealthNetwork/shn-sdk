# Security policy

## Reporting a vulnerability

Please report suspected vulnerabilities **privately** — do not open a public
issue. Use GitHub's private vulnerability reporting for this repository:
[github.com/SmartHealthNetwork/shn-sdk/security/advisories/new](https://github.com/SmartHealthNetwork/shn-sdk/security/advisories/new).
Reports filed there reach the repository's administrators and are visible to
nobody else until a fix and a disclosure timeline are agreed with you. If that
route isn't available to you, open a regular issue that says only that you have
a security report to make — no details — and a maintainer will open a private
advisory on your behalf and continue there.

Include what you'd normally include in a report: the affected version (the
`github.com/SmartHealthNetwork/shn-sdk` version in your `go.mod`, or the
`@vX.Y.Z` you installed the `shn` CLI from), a description of the issue, and —
if you have one — a minimal reproduction. We'll acknowledge receipt,
investigate, and coordinate a fix and disclosure timeline with you.

## Scope

This policy covers the code in this repository: the published `shn-sdk` Go
module and the `shn` CLI it ships. Reports about the Smart Health Network's
hosted services (the Hub, Authorization Framework, registrar, accounts service
and related trust-plane infrastructure) are welcome through the same advisory
link — there is no separate public channel for them — and are routed to the
right people internally.

The SDK is exercised against a preview network with **synthetic data only**. A
report that depends on real patient data being present describes a deployment
outside this policy's scope; we still want to hear about anything in the SDK's
code or defaults that would put such data at risk once real deployments exist.

## Our posture in one line

You hold your own keys: the SDK signs and seals on your side, the Hub routes
sealed envelopes it cannot read, and the only thing the network learns is who
sent what to whom — so most "data exposure" reports are about the caller's own
deployment; we still want to hear about anything in the SDK that could weaken
that guarantee, such as a signing, sealing, replay or authorization check that
can be bypassed.
