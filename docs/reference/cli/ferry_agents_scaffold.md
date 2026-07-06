## ferry agents scaffold

Set a project repo up for the multi-tool agent pipeline

### Synopsis

Set a project repo up for the multi-tool agent pipeline.

Both modes create the local-only runtime layout: .work.local/{scratch,logs},
hidden via the git info/exclude mechanism (never committed; .gitignore is
never touched).

Default mode (your own repo — tracked files): an AGENTS.md router stamped from
the config repo's template ({{PROJECT}}/{{DATE}} substituted), CLAUDE.md and
GEMINI.md as relative symlinks to AGENTS.md inside the repo, a committed
.work/ (NEXT.md, DECISIONS.md), the docs/ hierarchy with its map
(docs/README.md), and a pre-commit config when the repo has none.

--attribution (tracked mode only) marks a repo that REQUIRES AI disclosure,
overriding the workspace no-attribution default: it installs the
prepare-commit-msg hook (a kernel-style Assisted-by trailer on agent-authored
commits — never Co-Authored-By), sets core.hooksPath to .githooks (per clone),
and appends the AI-attribution section to AGENTS.md.

--private mode (a repo you don't own — zero tracked trace): .work.local/ only
(NEXT.md, DECISIONS.md, ISSUES.md). No AGENTS.md, no symlinks, no docs, no
tracked file touched. Not combinable with --attribution.

Idempotent; never overwrites an existing file.

```
ferry agents scaffold <repo-dir> [name] [flags]
```

### Options

```
      --attribution   this repo requires AI disclosure: install the Assisted-by commit hook and AGENTS.md policy section
  -h, --help          help for scaffold
      --private       leave no tracked trace: .work.local/ only, excluded via git info/exclude
```

### SEE ALSO

* [ferry agents](ferry_agents.md)	 - Onboard project repos and migrate agent-instruction setups

