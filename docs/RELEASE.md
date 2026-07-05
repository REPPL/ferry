# Cutting a release

Cutting a release is what enables the public `curl … | bash` install path. Until a
release exists, that path deliberately refuses to install (fail-closed): the installer
has no `checksums.txt` to fetch and verify against. Build-from-source works today
without any of this: see [Getting started](getting-started.md).

## Versioning

ferry uses [Semantic Versioning](https://semver.org) with a leading `v`:
`vMAJOR.MINOR.PATCH`. The first release is **`v0.1.0`**. While the version is `0.y.z`
the CLI surface may still change between minor versions (pre-1.0 = not yet API-stable);
`v1.0.0` marks the first stable surface. The tag drives everything: the release
workflow triggers on `v*`, and the tag is stamped into the binary via `-ldflags` so
`ferry --version` reports it (e.g. `ferry v0.1.0`). Un-tagged local builds report the
current development line (`v0.4.0-dev`).

Checksums are **automated**, not hand-pasted. A script computes the SHA256 of each
binary into a `checksums.txt` manifest that ships as a release asset; CI runs that
script on a tag push. `install.sh` fetches `checksums.txt` from the release it is
installing and verifies each download against it — no checksum is ever committed back
to a branch.

## Automated flow (primary)

Push a version tag and CI does the rest:

```bash
git tag vX.Y.Z
git push origin vX.Y.Z          # or: git push --follow-tags
```

The [`release` workflow](../.github/workflows/release.yml) then, for the tag:

1. Cross-compiles the four `bin/ferry-<goos>-<arch>` binaries (`make build`).
2. Runs [`scripts/gen-checksums.sh`](../scripts/gen-checksums.sh), which writes the real
   SHA256 of each binary into a `bin/checksums.txt` manifest (`sha256sum` format).
3. Attests build provenance for the four binaries **and** `checksums.txt` — a signed
   [SLSA build-provenance](https://slsa.dev/) attestation per artefact.
4. Creates the GitHub Release for the tag and uploads the four `bin/ferry-*` binaries
   plus `checksums.txt` as release assets.
5. Proves the attestation by downloading a binary fresh from the new Release and running
   `gh attestation verify` against it. A failed attestation or verification fails the
   release.

The workflow pushes nothing to any branch. It records the remote default-branch tip at
the start of the release job and asserts it is unchanged at the end, so a step that ever
reintroduced a branch push would fail the run.

The result is a verified release whose `checksums.txt` matches the published assets and
whose binaries carry a verifiable provenance attestation: no manual checksum paste
anywhere.

## Provenance attestations

Each released binary — and `checksums.txt` itself — has a signed build-provenance
attestation linking it to the commit and workflow run that built it. `install.sh` does
not consume these attestations (it verifies the binary against `checksums.txt`);
attesting the manifest makes it a first-class artefact anyone can verify. Users verify a
download with the GitHub CLI:

```bash
gh attestation verify ferry-<goos>-<arch> -R REPPL/ferry
```

This is a genuine signature over the artefact (unlike the in-transit checksum below),
so it detects a binary that was not produced by this repository's release workflow.

## Release retention

Each release line keeps only its newest release. When a new `vX.Y.Z` publishes, the
workflow runs [`scripts/prune-releases.sh`](../scripts/prune-releases.sh), which keeps
the latest release of the current line (`X.Y`) plus the last release of every other
line, and deletes the superseded releases: the GitHub Release and its binary assets.
Git tags are immutable once pushed and are never deleted, so a pruned version's tag —
and the commit it points at — stays reachable. So shipping `v0.1.2` removes the
`v0.1.1` release, while shipping `v0.2.0` keeps the last `v0.1.x` alongside it. Run it
by hand with `--dry-run` to preview:

```bash
scripts/prune-releases.sh --current vX.Y.Z --dry-run
```

The release just published is never pruned, and pruning refuses if a newer patch than
the current one already exists.

## Local / fallback flow

To prepare a release-ready tree locally (e.g. to inspect the manifest before tagging, or
if you publish by hand):

```bash
make release
```

`make release` builds the binaries and runs `gen-checksums` (`scripts/gen-checksums.sh`),
which writes `bin/checksums.txt` over the built binaries: idempotent and re-runnable. It
then prints how to publish (push a tag for CI, or create the Release and upload the
`bin/ferry-*` binaries and `bin/checksums.txt` yourself).

You can also run the pieces directly:

```bash
make build            # cross-compile the four binaries
make checksums        # write bin/checksums.txt over them
```

To publish by hand, verify a download against the manifest the same way `install.sh`
does — from a directory holding the asset and `checksums.txt`:

```bash
shasum -a 256 -c checksums.txt   # or: sha256sum -c checksums.txt
```

## Why the manifest matters

`install.sh` is **fail-closed**: with no fetchable `checksums.txt`, no entry for the
selected target, or a hash mismatch, it refuses to install, so the network path never
installs an unverified binary. When the manifest is present, the installer hashes the
download and compares it to the manifest entry, catching corruption or tampering **in
transit**.

Be honest about the scope: this is **not** a full supply-chain guarantee. `checksums.txt`
ships from the same unauthenticated source as the binary, so a compromised source could
serve a matching pair. Treat it as a personal-trust convenience, not a signature — the
build-provenance attestation above is the real signature.

## Related Documentation

- [`scripts/gen-checksums.sh`](../scripts/gen-checksums.sh): writes the `checksums.txt` manifest.
- [`.github/workflows/release.yml`](../.github/workflows/release.yml): tag-triggered build → checksum → attest → publish.
- [`install.sh`](../install.sh): the installer that fetches and verifies `checksums.txt`.
- [README—Install](../README.md#install): the user-facing install command.
- [Getting started](getting-started.md): build-from-source, which needs no release.
