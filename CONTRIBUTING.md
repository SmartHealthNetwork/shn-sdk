# Contributing

This repository is a **published snapshot** of the SDK as it is developed inside
the Smart Health Network's internal platform repository — one commit per
release, not a tree built up from pull requests merged directly here. If you
send a PR, we'll read it, but the actual change lands internally and reaches
this repository on the next publish. Please don't be surprised if we ask you to
describe the change instead of reviewing a diff.

## How to reach us

- **Bugs, questions and feature requests:** open a GitHub issue on this
  repository. See [`SUPPORT.md`](SUPPORT.md) for what to include.
- **Security issues:** do **not** open a public issue — see
  [`SECURITY.md`](SECURITY.md) for private reporting.
- **Partner integration and account questions:** use the same channel you
  used to request your developer account, or open an issue.

## Versioning and stability

`shn-sdk` follows semantic versioning. From v1.0.0 it is at major version 1:

- **MINOR** versions (1.x.0 → 1.(x+1).0) may add to the SDK and never break
  what the compatibility promise below covers. They may change the Go API, which it
  does not cover; the release notes name each such change.
- **PATCH** versions (1.x.y → 1.x.(y+1)) contain backwards-compatible fixes
  only.
- A **MAJOR** version would be the only release that breaks what the promise
  covers.

A published version tag is **never re-tagged** with different content: the Go
module proxy caches a tag's tree permanently, so a fix always ships as a new
version, and a version that must not be used is marked `retract` in `go.mod`
rather than deleted. The release history is at
[github.com/SmartHealthNetwork/shn-sdk/releases](https://github.com/SmartHealthNetwork/shn-sdk/releases).
`shn-gateway` pins a version of this module; its `go.mod` is the compatibility
record between the two.

The module's Go API carries **no compatibility promise**: the Smart Gateway
compiles its builders, and partners use the network through the gateway's
published binary and the `shn` CLI. A release may change or remove a Go symbol;
after v1.0.0, within a major, such a change comes in a minor release, never a
patch, and that release's notes name each incompatible change in their
"Go API" section.
From v1.0.0 the compatibility promise covers the `shn` CLI's onboarding
commands (`keygen`, `register`, `login`, `clients`, `revoke`, `rotate`,
`doctor`: their flags, exit codes and the key files they write; see the
[README](README.md)) and the gateway's binary contract (see the gateway's
`STABILITY.md`), not the Go API.

## Licensing

This repository is licensed under Apache-2.0 (see [`LICENSE`](LICENSE)). By
submitting a change you agree that it is contributed under that license.

## What a contribution looks like here

The most useful contributions are precise reports: a wire vector that the SDK
mis-handles (`testdata/vectors/` is the SDK's hermetic conformance contract),
a `shn doctor` transcript that disagrees with the documentation, or a section of
[`docs/PARTICIPANT_PROTOCOL.md`](docs/PARTICIPANT_PROTOCOL.md) that a
direct-integration implementer could not follow. Each of those turns into an
internal change with a test, and the test is what ships.

## Documentation vocabulary

The docs in this repository are written for participants, in the network's
canonical terms: **Hub**, **Smart Gateway**, **Authorization Framework**,
**Federated Query Services**, **PHG**, **Global Person Consent**, **Audit
Plane**, **holder**, **direct operator**. If you propose a wording change, keep
to those terms and to the concrete (a real endpoint, a real env var, a real
error message) over an abstract description of "the network".
