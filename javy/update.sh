#!/usr/bin/env bash
# Downloads the default plugin of a Javy release, unmodified, into plugin/,
# and Javy's license into LICENSE, which covers this whole `javy/` module.
# javy.go embeds these files.
#
# Usage: ./update.sh [version]
#
# Without a version (e.g. v9.1.0), it takes the latest release.
set -euo pipefail

repo=bytecodealliance/javy
module="$(cd "$(dirname "$0")" && pwd)"
dir="$module/plugin"

version=${1:-}
if [ -z "$version" ]; then
	version=$(curl -fsSL "https://api.github.com/repos/$repo/releases/latest" |
		sed -n 's/^ *"tag_name": *"\([^"]*\)".*/\1/p')
fi
if [ -z "$version" ]; then
	echo "cannot determine the latest Javy release" >&2
	exit 1
fi

# The module's version is the Javy version it embeds, so Javy's major version
# must end the module path, e.g. github.com/skybitsnl/go-applets/javy/v9.
major=${version#v}
major=${major%%.*}
path=$(sed -n 's/^module //p' "$module/go.mod")
if [ "${path##*/}" != "v$major" ]; then
	cat >&2 <<EOF
Javy $version has major version $major, but the module path in go.mod is

	$path

Change it to end in /v$major, update the import of this module and its require
and replace in the root module's go.mod, and run update.sh again.
EOF
	exit 1
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

release="https://github.com/$repo/releases/download/$version"
source="https://raw.githubusercontent.com/$repo/$version"
curl -fsSL -o "$tmp/plugin.wasm.gz" "$release/plugin.wasm.gz"
curl -fsSL -o "$tmp/plugin.wasm.gz.sha256" "$release/plugin.wasm.gz.sha256"
curl -fsSL -o "$tmp/LICENSE.md" "$source/LICENSE.md"

# The checksum is published next to the plugin, so this catches a broken
# download, not a compromised release.
want=$(awk '{print $1}' "$tmp/plugin.wasm.gz.sha256")
if command -v sha256sum >/dev/null; then
	got=$(sha256sum "$tmp/plugin.wasm.gz" | awk '{print $1}')
else
	got=$(shasum -a 256 "$tmp/plugin.wasm.gz" | awk '{print $1}')
fi
if [ "$got" != "$want" ]; then
	echo "plugin.wasm.gz: sha256 is $got, release says $want" >&2
	exit 1
fi

# Apache-2.0 requires passing on a NOTICE file, if Javy ever adds one.
rm -f "$module/NOTICE"
if curl -fsSL -o "$tmp/NOTICE" "$source/NOTICE" 2>/dev/null; then
	echo "Javy $version has a NOTICE file; it is included as NOTICE." >&2
	mv "$tmp/NOTICE" "$module/"
fi

mkdir -p "$dir"
mv "$tmp/plugin.wasm.gz" "$dir/"
mv "$tmp/LICENSE.md" "$module/LICENSE"
echo "$version" >"$dir/VERSION"
echo "Updated to Javy $version (sha256 of plugin.wasm.gz: $got)."
