#!/usr/bin/env bash
# Tests of lab-pin.sh require-release and require-candidate: .github/scripts/lab-pin.test.sh (git only; the laboratory remote is a local repository).
set -uo pipefail
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
fail=0
check() { # name expected actual
  if [[ "$2" == "$3" ]]; then echo "ok   $1"; else echo "FAIL $1: want '$2', got '$3'"; fail=1; fi
}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t

# the "laboratory" remote: tags v1.11.0-rc.1 and v1.10.0, a branch named like a version
git init -q -b main "$tmp/lab"
git -C "$tmp/lab" commit -q --allow-empty -m a
git -C "$tmp/lab" tag v1.11.0-rc.1
git -C "$tmp/lab" tag v1.10.0
git -C "$tmp/lab" branch v1.12.0
export LAB_PIN_REMOTE="$tmp/lab"

# pin MODE VERSION: the exit code of lab-pin.sh MODE with a go.mod that requires that laboratory version
pin() {
  mkdir -p "$tmp/mod"
  printf 'module x\n\nrequire (\n\tgithub.com/cybericebox/laboratory %s\n)\n' "$2" > "$tmp/mod/go.mod"
  (cd "$tmp/mod" && bash "$here/lab-pin.sh" "$1" >/dev/null 2>&1; echo $?)
}

check "release: a release is accepted" 0 "$(pin require-release v1.10.0)"
check "release: a candidate is refused" 1 "$(pin require-release v1.11.0-rc.1)"
check "release: a pseudo-version is refused" 1 "$(pin require-release v1.10.1-0.20261123111509-08c1ffe8350a)"
check "release: a missing tag is refused" 1 "$(pin require-release v1.13.0)"
check "release: v0.0.0 is refused" 1 "$(pin require-release v0.0.0)"

check "candidate: a candidate is accepted" 0 "$(pin require-candidate v1.11.0-rc.1)"
check "candidate: a release is accepted" 0 "$(pin require-candidate v1.10.0)"
check "candidate: a pseudo-version is refused" 1 "$(pin require-candidate v1.10.1-0.20261123111509-08c1ffe8350a)"
check "candidate: a missing candidate tag is refused" 1 "$(pin require-candidate v1.11.0-rc.2)"
check "candidate: a branch is not a tag" 1 "$(pin require-candidate v1.12.0)"
check "candidate: v0.0.0 is refused" 1 "$(pin require-candidate v0.0.0)"
check "candidate: a build suffix is refused" 1 "$(pin require-candidate v1.11.0-rc.1+dirty)"
check "candidate: a beta is refused" 1 "$(pin require-candidate v1.11.0-beta.1)"

exit "$fail"
