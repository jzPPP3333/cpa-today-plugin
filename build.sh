#!/usr/bin/env bash
# 构建 today 插件动态库。
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT"

export CGO_ENABLED=1
mkdir -p dist

case "${GOOS:-$(go env GOOS)}" in
  windows) ext="dll" ;;
  darwin)  ext="dylib" ;;
  *)       ext="so" ;;
esac

go vet ./... 2>/dev/null || true
go build -buildmode=c-shared -trimpath -ldflags="-s -w" -o "dist/today.$ext" .
echo "built dist/today.$ext"
ls -la "dist/today.$ext"
