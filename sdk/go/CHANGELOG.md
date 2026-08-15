# Changelog — LIGHTWEIGHT Go SDK

Releases of `github.com/JoaoGabrielVianna/lightweight-saas-backend/sdk/go`.

**This file is only about the SDK module.** Server and repository changes are in
the [root CHANGELOG](../../CHANGELOG.md), and the two version streams are
deliberately separate: a server release says nothing about this module and this
module's version says nothing about the HTTP API. Keeping one list would make
"what changed in the SDK between the two versions I depend on" unanswerable
without reading everything else.

It lives here rather than at the repository root for a second, more practical
reason: the module zip contains `sdk/go/**`, so this file is distributed *with
the module* and is what a consumer sees on pkg.go.dev. A root changelog is not.

The format is [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). While the
major version is zero the Go API may change between minor versions; every such
change is listed under **Breaking**, and shows up as a removed line in
[`api.txt`](api.txt).

**Compatibility:** SDK v0.x targets LIGHTWEIGHT HTTP API `/v1`.

---

## [Unreleased]

Nothing has been released. The first release is expected to be `v0.1.0`, tagged
`sdk/go/v0.1.0`; see [docs/SDK_GO.md](../../docs/SDK_GO.md#the-first-release).

### Added

- Everything. This is the initial surface: `Client` with `Users`, `Roles`,
  `Sessions`, `Invitations` and `Audit`; construction from
  `LIGHTWEIGHT_URL` / `LIGHTWEIGHT_WORKSPACE_ID` / `LIGHTWEIGHT_API_KEY`;
  `APIError` / `RequestError` / `ProtocolError`; stable machine-readable error
  codes. The full list is [`api.txt`](api.txt).
- `Version()`, reporting the module version the consuming build resolved, read
  from the Go build record rather than a hard-coded constant. It is `"dev"` when
  this module is the main module — a test binary, or a local `replace`.
- Executable examples, so pkg.go.dev renders working code. None of them makes a
  network call.

---

## Release notes

An SDK release note should answer, in this order:

| | |
|---|---|
| **Version** | and the tag that published it |
| **Compatible server API** | today always `/v1` |
| **Breaking** | removals, renames, signature and behaviour changes — first, because it decides whether to upgrade |
| **Added** | new methods, fields, error codes |
| **Fixed** | bugs, with the symptom a consumer would have seen |
| **Security** | anything affecting credential handling, redaction, or TLS |

Written by hand. There is no prose generator here, and a generated list of commit
subjects would not answer the one question a consumer opens this file to ask.
