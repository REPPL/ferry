# Release plans

Reference for the design plans under `.abcd/development/plans/`.

Each plan captures the design for one release. A plan is named
`YYYY-MM-DD-vX.Y.Z.md`, where the date is when the plan was written and the
version is the release it describes.

## Frontmatter

Every plan opens with a prose frontmatter line (not YAML) that records the date
and the plan's status, for example:

```
Date: 2026-07-05 · Status: shipped in v0.4.0
```

## The `Status:` convention

A plan's `Status:` line reads `shipped in vX.Y.Z` once that release is out.
Before release it records the design state instead (for example
`design agreed`, `approved, awaiting implementation`).

`scripts/release.sh` and the `verify` job in
`.github/workflows/release.yml` enforce this through
`scripts/check-plan-shipped.sh`: a release refuses to tag while a plan matching
the version exists but is not marked shipped. A version with no plan is fine —
not every patch release has one.
