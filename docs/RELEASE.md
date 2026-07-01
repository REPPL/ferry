# Cutting a release

Cutting a release is what enables the public `curl … | bash` install path. Until a
release exists and its checksums are pinned, that path deliberately refuses to install
(fail-closed). Build-from-source works today without any of this: see
[Getting started](getting-started.md).

## Versioning

ferry uses [Semantic Versioning](https://semver.org) with a leading `v`:
`vMAJOR.MINOR.PATCH`. The first release is **`v0.1.0`**. While the version is `0.y.z`
the CLI surface may still change between minor versions (pre-1.0 = not yet API-stable);
`v1.0.0` marks the first stable surface. The tag drives everything: the release
workflow triggers on `v*`, and the tag is stamped into the binary via `-ldflags` so
`ferry --version` reports it (e.g. `ferry v0.1.0`). Un-tagged local builds report the
current development line (`v0.1.0-dev`).

Checksums are **automated**, not hand-pasted. A script computes the SHA256 of each
binary and writes it into `install.sh`; CI runs that script on a tag push.

## Automated flow (primary)

Push a version tag and CI does the rest:

```bash
git tag vX.Y.Z
git push origin vX.Y.Z          # or: git push --follow-tags
```

The [`release` workflow](../.github/workflows/release.yml) then, for the tag:

1. Cross-compiles the four `bin/ferry-<goos>-<arch>` binaries (`make build`).
2. Runs [`scripts/pin-checksums.sh`](../scripts/pin-checksums.sh), which pins the real
   SHA256 of each binary into `install.sh`'s `sha_*` variables.
3. Commits the pinned `install.sh` back to the default branch (only if it changed),
   as the `github-actions[bot]` identity.
4. Creates the GitHub Release for the tag and uploads the four `bin/ferry-*` binaries
   as release assets.

The result is a verified release whose pinned checksums match the published assets:
no manual checksum paste anywhere.

## Release retention

Each release line keeps only its newest release. When a new `vX.Y.Z` publishes, the
workflow runs [`scripts/prune-releases.sh`](../scripts/prune-releases.sh), which keeps
the latest release of the current line (`X.Y`) plus the last release of every other
line, and deletes the superseded releases: the GitHub Release, its binary assets, and
the git tag. So shipping `v0.1.2` removes `v0.1.1`, while shipping `v0.2.0` keeps the
last `v0.1.x` alongside it. Run it by hand with `--dry-run` to preview:

```bash
scripts/prune-releases.sh --current vX.Y.Z --dry-run
```

The release just published is never pruned, and pruning refuses if a newer patch than
the current one already exists.

## Local / fallback flow

To prepare a release-ready tree locally (e.g. to inspect the pins before tagging, or
if you publish by hand):

```bash
make release
```

`make release` builds the binaries and runs `pin-checksums` (`scripts/pin-checksums.sh`),
which edits `install.sh` in place: idempotent and re-runnable. It then prints how to
publish (push a tag for CI, or create the Release and upload `bin/ferry-*` yourself).

You can also run the pieces directly:

```bash
make build            # cross-compile the four binaries
make pin-checksums    # write real checksums into install.sh
scripts/pin-checksums.sh --check   # verify pins match fresh binaries (no edit); CI-friendly
```

`make checksums` still exists as a print-only helper (it lists the `sha_*` lines
without touching `install.sh`).

## Why the pins matter

`install.sh` is **fail-closed**: an empty pin for the selected target means "no
checksum → refuse to install", so the network path never installs an unverified
binary. Once pinned, the installer hashes the download and compares it to the pin,
catching corruption or tampering **in transit**.

Be honest about the scope: this is **not** a full supply-chain guarantee. The checksum
ships from the same unauthenticated source as the binary, so a compromised source could
serve a matching pair. Treat it as a personal-trust convenience, not a signature.

## Related Documentation

- [`scripts/pin-checksums.sh`](../scripts/pin-checksums.sh): writes/verifies the pins.
- [`.github/workflows/release.yml`](../.github/workflows/release.yml): tag-triggered build → pin → publish.
- [`install.sh`](../install.sh): the installer whose `sha_*` pins are filled automatically.
- [README—Install](../README.md#install): the user-facing install command.
- [Getting started](getting-started.md): build-from-source, which needs no release.
