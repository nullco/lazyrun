#!/usr/bin/env bash
# Inspect both archives and exercise the native binary without starting services.
set -euo pipefail

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
version=${VERSION:-dev}
case "$version" in
  ''|*[!A-Za-z0-9._+-]*) echo 'Invalid VERSION' >&2; exit 2 ;;
esac
host_os=$(go env GOHOSTOS)
host_arch=$(go env GOHOSTARCH)
if [[ "$host_os" != linux || ! "$host_arch" =~ ^(amd64|arm64)$ ]]; then
  echo 'Package verification requires Linux amd64 or arm64' >&2
  exit 2
fi
stage=$(mktemp -d "${TMPDIR:-/tmp}/lazyrun-check-release.XXXXXXXX")
trap 'rm -rf -- "$stage"' EXIT
cd "$root/dist"
sha256sum -c "lazyrun_${version}_SHA256SUMS"

for arch in amd64 arm64; do
  mkdir "$stage/$arch"
  tar -xzf "lazyrun_${version}_linux_${arch}.tar.gz" -C "$stage/$arch"
  binary="$stage/$arch/lazyrun"
  test -x "$binary"
  test -f "$stage/$arch/LICENSE"
  test -f "$stage/$arch/third-party/Go-LICENSE"
  info=$(go version -m "$binary")
  grep -Fx $'\tbuild\tGOOS=linux' <<< "$info"
  grep -Fx $'\tbuild\tGOARCH='"$arch" <<< "$info"
  if [[ "$arch" == "$host_arch" ]]; then
    test "$("$binary" --version)" = "Version: $version"
    (cd "$stage/$arch/examples/flask" && "$binary" --check)
  fi
done
