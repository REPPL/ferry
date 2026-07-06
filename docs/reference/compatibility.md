# Compatibility contract

This reference states which of ferry's surfaces are stable, what a release may
change, and how ferry treats a state file written by a different version. It
describes what ferry guarantees; it prescribes no procedure.

## Compatibility surfaces

ferry has three surfaces a user or an automation depends on:

| Surface | What it is |
|---|---|
| **CLI** | The commands, subcommands, flags, exit codes, and the machine-relevant wording of their output. |
| **`ferry.toml` schema** | The manifest keys and value shapes documented in [configuration](../configuration.md) and [the agents domain](../agents.md) — `[manage]`, `[agents]`, and their tables. |
| **On-disk state** | The files ferry keeps under its state directory (`~/.local/state/ferry`): the last-applied store, the agents target record, the backup journal, and the immutable baseline. |

## The pre-1.0 rule

Before v1.0.0, a **minor** release may break any of the three surfaces. A
breaking change carries a **`Breaking`** entry in the changelog that names the
surface and the change. There is no deprecation window before v1.0.0: the
changelog entry is the whole notice.

One guarantee holds even pre-1.0: **defaults never change silently.** A change to
a default value — what a key means when it is absent, which domains or harnesses
are managed when a list is omitted — is itself a breaking change and carries its
own `Breaking` changelog entry. A machine that pins nothing still behaves
predictably across an upgrade, or the release says so in words.

## The deprecation contract from v1.0.0

From v1.0.0, a CLI or `ferry.toml` surface is removed only through deprecation:

- The **old form keeps working for at least one minor release** after it is
  deprecated. It is not removed in the same release that deprecates it.
- Using a deprecated form prints a **warning that names the replacement verbatim**
  — the exact command, flag, or key to use instead — so that an automation or an
  agent that hits the warning can correct itself from the message alone, without
  consulting the changelog.
- Removal happens no earlier than the following minor release, and carries a
  `Breaking` changelog entry.

## On-disk state versioning

Every state file ferry's domain machinery owns carries an integer `version` field
at the top level of its JSON. This lets a newer ferry recognise an older file and
a downgraded ferry recognise a file it cannot safely read.

The versioned files are:

| File | Contents |
|---|---|
| `dotfile-last-applied.json` | Per managed target, the content hash ferry last wrote — the middle term of the three-way apply comparison — and, from schema version 2, the exact bytes it last deployed (the *last-deployed baseline*), content-addressed by that hash so ferry can reconstruct and diff what it last wrote. |
| `agents-targets.json` | Every `$HOME` destination the agents domain has applied on this machine, so `ferry restore agents` reverts what was actually applied. |
| `journal/<run>/manifest.json` | One apply run's record of prior states and actions, used to roll back an interrupted run. |

The immutable **baseline** (ferry's record of true pre-ferry state, which
`restore` reverses to) is a separate, content-addressed store and is read
version-independently; it is not part of this envelope scheme.

### Reading an older file

A file with **no `version` field** is the original form that predates versioning
and reads as **version 1**. A file at a **versioned-but-older** version (for
example the last-applied store at version 1, once its current schema is version
2) is recognised the same way. Migration is **forward-only** and happens **on
read**, on a mutating command (`apply`, `capture`) — a read-only command
(`status`, `diff`) reads the older form in memory and writes nothing. An older
last-applied store migrates forward with every recorded hash preserved; the
last-deployed baseline it gains starts empty and is re-established target by
target on the next apply.

Before a migrating command rewrites a file into the current form, it **preserves
the pre-migration bytes** in a sibling copy named `<file>.pre-v<n>.bak`, where
`<n>` is the version being migrated away from (for example
`dotfile-last-applied.json.pre-v1.bak`). The backup is written once and never
overwritten, so the first, genuinely-original copy is the one kept. This
migration copy is distinct from the backup **baseline**: it preserves ferry's own
bookkeeping, which is not managed `$HOME` state and never enters the baseline a
`restore` reverses.

The last-applied store and the agents target record follow this
back-up-then-rewrite path. The **journal manifest is the exception**: a journal
run is per-apply and short-lived, so an older manifest is never rewritten in
place. An older *complete* run stays in its original form (it is inert — restore
reverses through the baseline, not the journal), and an older *interrupted* run
is rolled back and cleared as usual.

### Reading a newer file

A file whose `version` is **higher than the running ferry understands** is
**refused, not read and not rewritten**. The command stops with an error that
names the file and both versions — the version found and the highest this ferry
supports — and the file is left byte-for-byte untouched. Upgrading ferry to a
version that understands the file is the way forward. A downgraded ferry can
therefore never silently corrupt state that a newer ferry wrote.

This refusal covers the interrupted-run path too: an incomplete journal run whose
manifest a newer ferry wrote is neither rolled back nor deleted — it is reported
and left in place.
