#!/bin/sh
# Download a published release. No sudo, shell profile edits, or service starts.
set -eu

fail() {
    printf 'lazyrun installer: %s\n' "$*" >&2
    exit 1
}

# Keep installation inside a function so a truncated piped script cannot begin it.
main() {
    for tool in uname curl grep awk sha256sum tar mktemp install mkdir mv rm; do
        command -v "$tool" >/dev/null 2>&1 || fail "Required command not found: $tool"
    done
    [ "$(uname -s)" = Linux ] || fail 'Only Linux is supported'
    case "$(uname -m)" in
        x86_64|amd64) arch=amd64 ;;
        aarch64|arm64) arch=arm64 ;;
        *) fail 'Only amd64 (x86_64) and arm64 (aarch64) are supported' ;;
    esac

    version=${LAZYRUN_VERSION:-latest}
    install_dir=${LAZYRUN_INSTALL_DIR:-${HOME:?Set HOME or LAZYRUN_INSTALL_DIR}/.local/bin}
    base=https://github.com/nullco/lazyrun/releases
    if [ "$version" = latest ]; then
        latest_url=$(curl -q -fsSL --proto '=https' --proto-redir '=https' \
            --retry 3 --connect-timeout 10 --max-time 120 \
            -o /dev/null -w '%{url_effective}' "$base/latest") \
            || fail 'Cannot resolve the latest stable release; has one been published?'
        case "$latest_url" in
            "$base/tag/"*) version=${latest_url##*/} ;;
            *) fail 'Unexpected latest release URL' ;;
        esac
    fi
    printf '%s\n' "$version" | grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-(alpha|beta|rc)\.(0|[1-9][0-9]*))?$' \
        || fail 'Invalid version; use vMAJOR.MINOR.PATCH or a tagged alpha/beta/rc release'
    # Reject embedded newlines as well as other invalid tag characters.
    case "$version" in
        *[!A-Za-z0-9.-]*) fail 'Invalid version characters' ;;
    esac

    umask 077
    stage=$(mktemp -d "${TMPDIR:-/tmp}/lazyrun-install.XXXXXXXX")
    install_stage=
    trap 'rm -rf -- "$stage"; if [ -n "$install_stage" ]; then rm -f -- "$install_stage"; fi' 0
    trap 'exit 1' HUP INT TERM
    archive="lazyrun_${version}_linux_${arch}.tar.gz"
    sums="lazyrun_${version}_SHA256SUMS"
    printf 'Downloading lazyrun %s for linux/%s...\n' "$version" "$arch"
    for asset in "$archive" "$sums"; do
        curl -q -fsSL --proto '=https' --proto-redir '=https' \
            --retry 3 --connect-timeout 10 --max-time 120 \
            -o "$stage/$asset" "$base/download/$version/$asset" \
            || fail "Cannot download $asset; check that the release is published"
    done

    checksum=$(awk -v asset="$archive" 'NF == 2 && $2 == asset { print $1 }' "$stage/$sums")
    [ "${#checksum}" -eq 64 ] || fail 'Missing or ambiguous archive checksum'
    case "$checksum" in
        *[!0-9a-fA-F]*) fail 'Invalid archive checksum' ;;
    esac
    printf '%s  %s\n' "$checksum" "$archive" > "$stage/selected-checksum"
    (cd "$stage" && sha256sum -c selected-checksum) || fail 'Archive checksum verification failed'
    tar -xzf "$stage/$archive" -C "$stage" --no-same-owner --no-same-permissions lazyrun \
        || fail 'Cannot extract lazyrun from the archive'
    [ -f "$stage/lazyrun" ] && [ ! -L "$stage/lazyrun" ] || fail 'Archive must contain a regular lazyrun binary'

    mkdir -p -- "$install_dir"
    [ ! -d "$install_dir/lazyrun" ] || fail 'Install destination is a directory'
    # Stage on the destination filesystem and rename atomically, preserving an
    # existing installation if downloading, verification, or staging fails.
    install_stage=$(mktemp "$install_dir/.lazyrun.XXXXXXXX")
    install -m 0755 -- "$stage/lazyrun" "$install_stage"
    mv -fT -- "$install_stage" "$install_dir/lazyrun"
    install_stage=
    printf 'Installed lazyrun %s to %s/lazyrun\n' "$version" "$install_dir"
    case ":${PATH:-}:" in
        *":$install_dir:"*) ;;
        *) printf 'Add this directory to your PATH: %s\n' "$install_dir" ;;
    esac
}

main "$@"
