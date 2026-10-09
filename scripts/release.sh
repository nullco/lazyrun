#!/usr/bin/env bash
# Local packaging only: never tags, pushes, publishes, or signs a release.
set -euo pipefail

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
version=${VERSION:-dev}
case "$version" in
  ''|*[!A-Za-z0-9._+-]*) echo 'VERSION must contain only letters, digits, dot, _, + or -' >&2; exit 2 ;;
esac
cd "$root"
umask 022
stage=$(mktemp -d "${TMPDIR:-/tmp}/lazyrun-release.XXXXXXXX")
trap 'rm -rf -- "$stage"' EXIT
mkdir -p dist
# Explicit allowlist: do not copy local venvs, brokers, bytecode or secrets.
mkdir -p "$stage/examples/flask" "$stage/examples/smoke"
cp README.md RELEASE.md LICENSE "$stage/"
cp examples/flask/lazyrun.yml "$stage/examples/flask/"
cp examples/smoke/{README.md,lazyrun.yml,smoke_app.py,requirements.txt} "$stage/examples/smoke/"
chmod 0644 "$stage/README.md" "$stage/RELEASE.md" "$stage/LICENSE" "$stage/examples/"*/*
chmod 0755 "$stage/examples" "$stage/examples/"*

# Dependencies retain their own license/NOTICE texts alongside lazyrun's MIT license.
mkdir -p "$stage/third-party"
cp "$(go env GOROOT)/LICENSE" "$stage/third-party/Go-LICENSE"
printf 'Go runtime: %s\nLinked module license/NOTICE texts:\n' "$(go env GOVERSION)" > "$stage/third-party/README.txt"
for arch in amd64 arm64; do
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go list -deps \
    -f '{{if and .Module (not .Module.Main)}}{{.Module.Path}}|{{.Module.Version}}|{{.Module.Dir}}{{end}}' \
    ./cmd/lazyrun >> "$stage/modules.raw"
done
LC_ALL=C sort -u "$stage/modules.raw" > "$stage/modules.list"
while IFS='|' read -r module revision dir; do
  [[ -n "$module" ]] || continue
  dest="$stage/third-party/${module//\//_}_$revision"
  mkdir -p "$dest"
  found=false
  for notice in "$dir"/LICENSE* "$dir"/COPYING* "$dir"/NOTICE*; do
    [[ -f "$notice" ]] || continue
    cp "$notice" "$dest/"
    found=true
  done
  if [[ "$found" != true ]]; then echo "Missing dependency license: $module" >&2; exit 1; fi
  chmod 0644 "$dest/"*
  chmod 0755 "$dest"
  printf '%s %s\n' "$module" "$revision" >> "$stage/third-party/README.txt"
done < "$stage/modules.list"
chmod 0644 "$stage/third-party/Go-LICENSE" "$stage/third-party/README.txt"
chmod 0755 "$stage/third-party"

for arch in amd64 arm64; do
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -buildvcs=false \
    -ldflags "-s -w -X main.version=$version" -o "$stage/lazyrun" ./cmd/lazyrun
  chmod 0755 "$stage/lazyrun"
  tar --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner \
    -cf - -C "$stage" lazyrun README.md RELEASE.md LICENSE examples third-party \
    | gzip -n > "dist/lazyrun_${version}_linux_${arch}.tar.gz"
done
(
  cd dist
  sha256sum "lazyrun_${version}_linux_amd64.tar.gz" "lazyrun_${version}_linux_arm64.tar.gz" \
    > "lazyrun_${version}_SHA256SUMS"
  sha256sum -c "lazyrun_${version}_SHA256SUMS"
)
