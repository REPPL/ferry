#!/usr/bin/env bash
# ferry end-to-end smoke test — drives the real binary through
# init -> diff -> apply -> idempotent apply -> capture -> restore in a THROWAWAY $HOME
# (mktemp; never touches the real ~). Asserts observable outcomes incl. restore
# returning managed files byte-identical to pre-ferry state and ~/.ssh untouched.
# Usage: scripts/smoke-e2e.sh /path/to/ferry-binary
set -uo pipefail
FERRY="$1"; H="$(mktemp -d)"; export HOME="$H"
fail=0; ok(){ echo "  OK  $*"; }; bad(){ echo "  XX  FAIL: $*"; fail=1; }; step(){ echo; echo "### $*"; }
SRC="$H/src"; mkdir -p "$SRC/dotfiles"
( cd "$SRC" && git init -q && git config user.email t@t && git config user.name t )
printf 'export FERRY_SMOKE=shared\nalias ll="ls -la"\n' > "$SRC/dotfiles/zshrc"
printf '[manage]\ndotfiles = [".zshrc"]\n' > "$SRC/ferry.toml"
( cd "$SRC" && git add -A && git commit -qm seed )
printf '# original\nexport ORIGINAL=1\n' > "$H/.zshrc"; ORIG="$(shasum "$H/.zshrc"|awk '{print $1}')"
mkdir -p "$H/.ssh"; echo "TRIPWIRE" > "$H/.ssh/config"; SSH0="$(shasum "$H/.ssh/config"|awk '{print $1}')"

step "1. init (clone file://)"
"$FERRY" init "file://$SRC" </dev/null >"$H/o1" 2>&1; [ $? -eq 0 ] && ok "init exit 0" || { bad "init nonzero"; sed 's/^/    /' "$H/o1"; }
CFG="$H/.config/ferry/config.toml"; [ -f "$CFG" ] && ok "config.toml written" || bad "no config.toml"
REPO="$(sed -n 's/^repo *= *"\(.*\)"/\1/p' "$CFG" 2>/dev/null)"; echo "    repo: $REPO"
[ -n "$REPO" ] && [ -f "$REPO/dotfiles/zshrc" ] && ok "clone has dotfiles/zshrc" || bad "clone incomplete"

step "2. diff (read-only)"
"$FERRY" diff </dev/null >"$H/o2" 2>&1
[ "$(shasum "$H/.zshrc"|awk '{print $1}')" = "$ORIG" ] && ok "diff left ~/.zshrc unchanged" || bad "diff mutated ~/.zshrc"

step "3. apply"
"$FERRY" apply </dev/null >"$H/o3" 2>&1; ac=$?; [ $ac -eq 0 ] && ok "apply exit 0" || { bad "apply exit $ac"; sed 's/^/    /' "$H/o3"; }
grep -q FERRY_SMOKE "$H/.zshrc" && ok "shared zshrc deployed" || bad "not deployed"
[ -d "$H/.local/state/ferry/baseline" ] && ok "baseline recorded" || bad "no baseline"

step "4. apply idempotent"
M1="$(stat -f %m "$H/.zshrc")"; sleep 1; "$FERRY" apply </dev/null >"$H/o4" 2>&1; M2="$(stat -f %m "$H/.zshrc")"
[ "$M1" = "$M2" ] && ok "second apply no-op" || bad "second apply rewrote"

step "5. capture (empty stdin, no auto-commit)"
C0="$(cd "$REPO" && git rev-list --count HEAD)"; printf 'export LOCAL_EDIT=1\n' >> "$H/.zshrc"
printf '' | "$FERRY" capture >"$H/o5" 2>&1; echo "    capture exit $?"
C1="$(cd "$REPO" && git rev-list --count HEAD)"; [ "$C0" = "$C1" ] && ok "no auto-commit" || bad "auto-committed"

step "6. restore"
printf 'y\n' | "$FERRY" restore --yes >"$H/o6" 2>&1; rc=$?; [ $rc -eq 0 ] && ok "restore exit 0" || { bad "restore exit $rc"; sed 's/^/    /' "$H/o6"; }
[ "$(shasum "$H/.zshrc"|awk '{print $1}')" = "$ORIG" ] && ok "restored byte-identical to pre-ferry original" || { bad "restore mismatch"; sed 's/^/      /' "$H/.zshrc"; }

step "7. ~/.ssh untouched"
[ "$(shasum "$H/.ssh/config"|awk '{print $1}')" = "$SSH0" ] && ok "~/.ssh/config unchanged" || bad "~/.ssh changed"

echo; [ $fail -eq 0 ] && echo "SMOKE VERDICT: PASS" || echo "SMOKE VERDICT: FAIL"
rm -rf "$H"; exit $fail
