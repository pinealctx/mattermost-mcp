#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
: "${GO:=go}"
: "${VERSION:?Set VERSION to the release tag}"
mkdir -p dist
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do
    target_os=${target%/*}
    target_arch=${target#*/}
    artifact="mattermost-mcp-${target_os}-${target_arch}"
    suffix=''
    if [ "$target_os" = windows ]; then suffix='.exe'; fi
    CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" "$GO" build -trimpath -ldflags "-s -w -X main.Version=${VERSION}" -o "dist/${artifact}${suffix}" .
    if [ "$target_os" = windows ]; then
        (cd dist && zip -q "${artifact}.zip" "${artifact}${suffix}")
    else
        tar -C dist -czf "dist/${artifact}.tar.gz" "$artifact"
    fi
    rm "dist/${artifact}${suffix}"
done
(cd dist && shasum -a 256 ./*.tar.gz ./*.zip > checksums.txt)
