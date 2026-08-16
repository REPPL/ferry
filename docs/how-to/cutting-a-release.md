# Cutting a release

Cutting a release is what enables the public `curl … | bash` install path. Until a
release exists, that path deliberately refuses to install (fail-closed): the installer
has no `checksums.txt` to fetch and verify against. Build-from-source works today
without any of this: see [Getting started](../tutorials/getting-started.md).

## Versioning

ferry uses [Semantic Versioning](https://semver.org) with a leading `v`:
`vMAJOR.MINOR.PATCH`. The first release is **`v0.1.0`**. While the version is `0.y.z`
the CLI surface may still change between minor versions (pre-1.0 = not yet API-stable);
`v1.0.0` marks the first stable surface. The tag drives everything: the release
workflow triggers on `v*`, and the tag is stamped into the binary via `-ldflags` so
`ferry --version` reports it (e.g. `ferry v0.1.0`). Un-tagged local builds report the
current development line (`v0.11.0-dev`).

Checksums are **automated**, not hand-pasted. A script computes the SHA256 of each
binary into a `checksums.txt` manifest that ships as a release asset; CI runs that
script on a tag push. `install.sh` fetches `checksums.txt` from the release it is
installing and verifies each download against it — no checksum is ever committed back
to a branch.

## Automated flow (primary)

Pushing the CHANGELOG promotion commit to `main` **is** the release act. The
[`auto-release` workflow](../../.github/workflows/auto-release.yml) runs on every push
to `main`: when the newest dated `## [X.Y.Z] - <date>` CHANGELOG heading has no git
tag yet, it creates the annotated `vX.Y.Z` tag at that commit and invokes the
[`release` workflow](../../.github/workflows/release.yml) directly as a reusable
workflow. An ordinary push — no newly promoted CHANGELOG heading — tags nothing and
does nothing. Tags are immutable: only the newest dated version is ever tagged, and an
already-tagged version is re-released only if its GitHub Release is missing, still
a draft, or short of its five assets (a transient publish failure), built from the
tagged commit, never a moved-on `main`.

Because the tag exists moments after the promotion commit lands, run the local gates
**before** pushing that commit. The driver itself cannot run at this point — its
first gate insists local `main` matches `origin/main`, which is false while the
promotion commit sits unpushed — so run its gates individually.
`docs-currency-lint` is maintainer-local tooling (it ships in the maintainer's
config repo, not in this repository) and runs only here, not in CI; on a
machine without it, skip that gate and rely on the others:

```bash
docs-currency-lint                                     # docs gate (CI does not run it)
scripts/check-plan-shipped.sh vX.Y.Z                   # plan marked shipped
make release VERSION=vX.Y.Z                            # build, version stamp, checksum manifest
scripts/prune-releases.sh --current vX.Y.Z --dry-run   # prune plan preview
```

A full driver run (`scripts/release.sh vX.Y.Z`) works only after the push, where it
stops at the already-created tag with every gate having run — the expected outcome
described under the [manual flow](#manual--recovery-flow) below.

The [`release` workflow](../../.github/workflows/release.yml) then, for the tagged
commit (its `verify` job re-runs `check-plan-shipped.sh` as a docs backstop, so a plan
left un-shipped fails the release even on a hand-pushed tag):

1. Cross-compiles the four `bin/ferry-<goos>-<arch>` binaries (`make build`).
2. Runs [`scripts/gen-checksums.sh`](../../scripts/gen-checksums.sh), which writes the real
   SHA256 of each binary into a `bin/checksums.txt` manifest (`sha256sum` format).
3. Attests build provenance for the four binaries **and** `checksums.txt` — a signed
   [SLSA build-provenance](https://slsa.dev/) attestation per artefact.
4. Creates the GitHub Release for the tag and uploads the four `bin/ferry-*` binaries
   plus `checksums.txt` as release assets. The release body is the tag's own dated
   CHANGELOG section, extracted verbatim, plus a link to the full CHANGELOG at that
   tag; a version whose section cannot be found publishes with an empty body rather
   than blocking the release.
5. Proves the published assets by downloading them fresh from the new Release: `gh
   attestation verify` against a binary, then `sha256sum -c checksums.txt` across the
   lot — the same pairing `install.sh` relies on. These checks run **after** the
   Release is public, so a failure fails the workflow *run*, not the release: the
   assets stay published and the red run is the signal to inspect them. See
   [post-publish recovery](#manual--recovery-flow) below.

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
workflow runs [`scripts/prune-releases.sh`](../../scripts/prune-releases.sh), which keeps
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

## Manual / recovery flow

**A tag whose release gate fails post-tag.** If a version tags but its `verify` run
goes red (the tag is immutable; `auto-release` retries the publish from the tagged
commit and stops after three failed runs), the version number is spent: the tagged
commit fails the gate deterministically and cannot be rebuilt from a fixed `main`.
Land the fix on `main`, promote the **next** patch version in the CHANGELOG, and let
the automatic flow ship that instead. Tags are never deleted (see the pruning rules
above), so the failed tag simply remains, release-less.

**A run that goes red after the Release is published.** The three post-publish steps —
the attestation verification, the fresh-download `sha256sum -c`, and the prune — run
once the Release is already public, so a failure there reddens the run without
unpublishing anything. Re-run the failed jobs: the publish step is idempotent (given an
existing Release it re-uploads the assets and re-asserts the title, notes, draft state
and prerelease flag), so the post-publish steps get another attempt against the same
release. The by-hand equivalents are the checksum check, from a directory holding the
assets downloaded fresh from the Release, and the prune, from the repository:

```bash
sha256sum -c checksums.txt                   # or: shasum -a 256 -c checksums.txt
scripts/prune-releases.sh --current vX.Y.Z   # add --dry-run to preview
```

When `auto-release` is disabled, or a tag must be cut by hand,
[`scripts/release.sh`](../../scripts/release.sh) is the blessed driver, run from a
clean `main` that is up to date with `origin/main`:

```bash
scripts/release.sh vX.Y.Z             # add --dry-run to rehearse without tagging
```

It fails closed at every gate before it creates anything: it asserts the `## [X.Y.Z]`
CHANGELOG section is promoted out of `[Unreleased]`, runs `docs-currency-lint`
(maintainer-local; on a machine without it, `FERRY_RELEASE_SKIP_DOCS_LINT=1`
skips that one gate explicitly and loudly),
requires any matching plan under `.abcd/development/plans/` to be marked
`shipped in vX.Y.Z` (via
[`scripts/check-plan-shipped.sh`](../../scripts/check-plan-shipped.sh)), and rehearses
the build, version stamp, checksum manifest, and prune plan. Only then does it prompt
(unless `--yes`) and run the single irreversible act: `git tag -a vX.Y.Z && git push
origin vX.Y.Z`. Note that on the automatic path above `auto-release` tags the
promotion commit as soon as it is pushed, so a driver run started afterwards fails at
its tag step on the existing tag — that is the expected outcome, not an error in the
release: the gates have still run, and the tag already points at the right commit.

After the tag push the driver does one last piece of housekeeping, all of it
local: it archives `.abcd/.work.local/NEXT.md` into `.abcd/.work.local/history/`
(as `NEXT-vX.Y.Z-<timestamp>.md`) and regenerates the file with its carry region
preserved. Under `--dry-run` it prints the current-to-regenerated diff instead of
writing anything, and a tree with no `NEXT.md` skips the step. Nothing here is
staged, committed, or pushed — `.abcd/.work.local/` is local-only.

To prepare a release-ready tree locally (e.g. to inspect the manifest before tagging, or
if you publish by hand):

```bash
make release VERSION=vX.Y.Z
```

`make release` builds the binaries and runs `gen-checksums` (`scripts/gen-checksums.sh`),
which writes `bin/checksums.txt` over the built binaries: idempotent and re-runnable.
Pass `VERSION=vX.Y.Z` whenever the tree is meant to match a release — omit it and the
binaries carry the in-source dev version, so their checksums cannot match the ones CI
publishes; omitting it is fine only for a mechanics check of the manifest. It then
points back at the two publishing paths — the promotion push for the automatic flow, or
`scripts/release.sh` as the recovery driver.

Or run the manifest step on its own. `checksums` rebuilds the four binaries first, so
it takes the same `VERSION` in the same invocation: a stamped build from an earlier,
separate `make build VERSION=vX.Y.Z` does not survive, because the rebuild replaces
those binaries with dev-stamped ones before hashing them.

```bash
make checksums VERSION=vX.Y.Z   # cross-compile the four binaries, then hash them
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

## Related documentation

- [`.github/workflows/auto-release.yml`](../../.github/workflows/auto-release.yml): tags the newest dated CHANGELOG version on push to `main` and calls the release workflow.
- [`scripts/release.sh`](../../scripts/release.sh): the manual release driver — gates, rehearses, then tags and pushes.
- [`scripts/check-plan-shipped.sh`](../../scripts/check-plan-shipped.sh): asserts a version's plan doc is marked shipped.
- [`scripts/gen-checksums.sh`](../../scripts/gen-checksums.sh): writes the `checksums.txt` manifest.
- [`.github/workflows/release.yml`](../../.github/workflows/release.yml): build → checksum → attest → publish; called by auto-release, or triggered directly by a hand-pushed tag.
- [`install.sh`](../../install.sh): the installer that fetches and verifies `checksums.txt`.
- [README—Install](../../README.md#install): the user-facing install command.
- [Getting started](../tutorials/getting-started.md): build-from-source, which needs no release.
