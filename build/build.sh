#!/bin/bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ARCH="${1:-x86}"
VERSION="$(awk -F'=' '/^version[[:space:]]*=/{gsub(/[[:space:]]/,"",$2);print $2}' "$ROOT/manifest")"
DIST="$ROOT/dist"
case "$ARCH" in
    x86|x86_64|amd64) ARCH=x86; PLATFORM=x86; GOARCH=amd64 ;;
    arm|arm64|aarch64) ARCH=arm; PLATFORM=arm; GOARCH=arm64 ;;
    *) echo "未知架构：$ARCH" >&2; exit 1 ;;
esac
info() { printf '\033[32m==>\033[0m %s\n' "$*"; }
die() { printf '\033[31m[✗]\033[0m %s\n' "$*" >&2; exit 1; }
GO_BIN="${GO_BIN:-$(command -v go 2>/dev/null || true)}"
if [ -z "$GO_BIN" ]; then
    for c in "$ROOT/.toolchain/go/bin/go" "$ROOT/../.toolchain/go/bin/go"; do [ -x "$c" ] && GO_BIN="$c" && break; done
fi
[ -n "$GO_BIN" ] || die "未找到 Go 工具链"
mkdir -p "$DIST" "$ROOT/app/bin" "$ROOT/.tmp"

# 前端完整性检查：init 依赖的函数必须存在（曾因误删导致整页事件失效）
for fn in analyze refreshStatus refreshSignals refreshOrders refreshLogs runScore saveSettings \
          toggleKill switchTab renderTargets readTargets renderCodeList refreshRecent syncVenue; do
    grep -q "function $fn" "$ROOT/src/webui/app.js" || die "app.js 缺少函数 $fn"
done
if command -v node >/dev/null 2>&1; then node --check "$ROOT/src/webui/app.js" || die "app.js 语法错误"; fi
info "编译 aitrader（GOARCH=$GOARCH）"
(cd "$ROOT/src" && CGO_ENABLED=0 GOARCH="$GOARCH" GOCACHE="${GOCACHE:-$ROOT/.tmp/gocache}" \
    GOPATH="${GOPATH:-$ROOT/.tmp/gopath}" TMPDIR="${TMPDIR:-$ROOT/.tmp}" GOFLAGS=-mod=mod GOPROXY=off \
    "$GO_BIN" build -trimpath -ldflags "-s -w" -o "$ROOT/.tmp/aitrader-$ARCH" .)
chmod 755 "$ROOT/.tmp/aitrader-$ARCH"
case "$(uname -m)" in aarch64|arm64) HOST=arm ;; *) HOST=x86 ;; esac
if [ "$ARCH" = "$HOST" ]; then cp "$ROOT/.tmp/aitrader-$ARCH" "$ROOT/app/bin/aitrader"; chmod 755 "$ROOT/app/bin/aitrader"; fi
STAGE="$DIST/stage-$ARCH"; PAYLOAD="$DIST/payload-$ARCH"
rm -rf "$STAGE" "$PAYLOAD"; mkdir -p "$STAGE" "$PAYLOAD/bin"
cp "$ROOT/manifest" "$STAGE/manifest"
sed -i "s/^platform[[:space:]]*=.*/platform              = $PLATFORM/" "$STAGE/manifest"
cp -a "$ROOT/config" "$ROOT/cmd" "$ROOT/wizard" "$STAGE/"
cp "$ROOT/ICON.PNG" "$ROOT/ICON_256.PNG" "$STAGE/"
cp "$ROOT/.tmp/aitrader-$ARCH" "$PAYLOAD/bin/aitrader"
cp -a "$ROOT/app/ui" "$PAYLOAD/ui"
find "$STAGE" -type d -exec chmod 755 {} +; find "$STAGE" -type f -exec chmod 644 {} +; chmod 755 "$STAGE"/cmd/*
find "$PAYLOAD" -type d -exec chmod 755 {} +; find "$PAYLOAD" -type f -exec chmod 644 {} +; chmod 755 "$PAYLOAD/bin/aitrader"
tar czf "$STAGE/app.tgz" -C "$PAYLOAD" --owner=0 --group=0 --numeric-owner --sort=name bin ui
chmod 644 "$STAGE/app.tgz"
OUT="$DIST/aitrader-${VERSION}-${ARCH}.fpk"; rm -f "$OUT"
tar czf "$OUT" -C "$STAGE" --owner=0 --group=0 --numeric-owner --sort=name manifest cmd config wizard ICON.PNG ICON_256.PNG app.tgz
chmod 644 "$OUT"
( cd "$DIST" && sha256sum "$(basename "$OUT")" > "$(basename "$OUT").sha256" )
info "产物：$OUT（$(du -h "$OUT" | cut -f1)）"
info "校验：$(cat "$OUT.sha256")"
python3 "$ROOT/tests/test_fpk.py" "$OUT" || die "打包自检未通过"
rm -rf "$STAGE" "$PAYLOAD"
