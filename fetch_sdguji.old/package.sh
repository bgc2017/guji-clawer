#!/usr/bin/env bash
# 交叉打包：在 macOS/Linux 上直接生成 Windows 可用的单文件 exe 和 zip。
# 用法: ./package.sh [版本号]   （默认用当天日期）
set -euo pipefail
cd "$(dirname "$0")"

VER="${1:-$(date +%Y%m%d)}"
rm -rf dist
mkdir -p dist

package() {
  local goarch="$1" pkg="windows-${1}"
  local dir="dist/${pkg}"
  mkdir -p "${dir}"
  echo "building ${pkg}..."
  GOOS=windows GOARCH="${goarch}" go build -trimpath -ldflags "-s -w" -o "${dir}/sdguji.exe" .
  cp packaging/README-Windows.txt "${dir}/README-Windows.txt"
  cp packaging/example.bat "${dir}/example.bat"
  (cd dist && zip -qr "sdguji-${pkg}-${VER}.zip" "${pkg}")
  echo "  -> dist/sdguji-${pkg}-${VER}.zip"
}

package amd64
package arm64

echo
echo "done:"
ls -lh dist/*.zip
