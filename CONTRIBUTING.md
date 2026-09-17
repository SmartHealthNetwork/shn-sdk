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

`shn-sdk` follows semantic versioning and is currently **pre-1.0 (0.x)**:

- **MINOR** versions (0.x.0 → 0.(x+1).0) may carry breaking changes. Each
  breaking change is called out in the release notes.
- **PATCH** versions (0.x.y → 0.x.(y+1)) contain backwards-compatible fixes
  only.

A published version tag is **never re-tagged** with different content: the Go
module proxy caches a tag's tree permanently, so a fix always ships as a new
version, and a version that must not be used is marked `retract` in `go.mod`
rather than deleted. The release history is at
[github.com/SmartHealthNetwork/shn-sdk/releases](https://github.com/SmartHealthNetwork/shn-sdk/releases).
`shn-gateway` pins a version of this module; its `go.mod` is the compatibility
record between the two.

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
