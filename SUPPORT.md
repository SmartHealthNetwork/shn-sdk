# Support

## Where to ask

| Need | Where |
|---|---|
| A bug in the SDK or the `shn` CLI, or a question about using them | A GitHub issue on this repository |
| A security vulnerability | **Not** an issue — see [`SECURITY.md`](SECURITY.md) |
| A developer account, client registration, or access to the preview network | The channel you used to request your developer account; if you don't have one yet, open an issue and we'll point you at it |
| A question about the wire protocol for a direct (non-Go) integration | [`docs/PARTICIPANT_PROTOCOL.md`](docs/PARTICIPANT_PROTOCOL.md) first, then an issue |

## What to include in an issue

- The SDK version: the `github.com/SmartHealthNetwork/shn-sdk` line in your
  `go.mod`, or the `@vX.Y.Z` you installed the `shn` CLI from.
- The `shn doctor` output when the problem is connectivity, registration or
  authorization — it prints the checks in order and stops at the first
  failure, which is usually the answer.
- What you sent and what came back, with **synthetic data only**. The preview
  network never carries real patient information, and neither should an issue.
- Whether the same request works with the reference walkthrough in
  [`docs/PREVIEW.md`](docs/PREVIEW.md).

## What to expect

This is a preview network and a pre-1.0 SDK: there is no support contract, no
uptime guarantee and no response-time commitment. Issues are read and answered
by the people who build the SDK; a confirmed defect becomes an internal change
with a test and ships in the next version (see
[`CONTRIBUTING.md`](CONTRIBUTING.md) for how releases work). Questions that turn
out to be documentation gaps get fixed in the documentation.
