#!/usr/bin/env bash
# 配布用 zip を作成する。
#   ./scripts/dist.sh           # tag/commit からバージョンを推定
#   VERSION=v1.2.3 ./scripts/dist.sh
#
# 出力: dist/bunashimeji-<version>.zip
#   bunashimeji/
#   ├── bunashimeji.exe
#   ├── README.md
#   ├── conf/   (空: ユーザが [キャラ名]/Actions.xml 等を配置)
#   └── img/    (空: ユーザが [キャラ名]/shime1.png 等を配置)
set -euo pipefail

cd "$(dirname "$0")/.."

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
DIST_DIR="dist"
STAGING="${DIST_DIR}/bunashimeji"
ZIP_NAME="bunashimeji-${VERSION}.zip"

rm -rf "${STAGING}" "${DIST_DIR}/${ZIP_NAME}"
mkdir -p "${STAGING}/conf" "${STAGING}/img"

# -H=windowsgui で起動時のコンソール窓を抑制 (Makefile の build ターゲットと揃える)。
GOOS=windows GOARCH=amd64 go build \
	-ldflags "-H=windowsgui -X main.version=${VERSION}" \
	-o "${STAGING}/bunashimeji.exe" .

cp README.md "${STAGING}/"

# zip は空ディレクトリも保持できるので、stating の構造をそのまま固める。
# zip の作業ディレクトリを dist/ に移してアーカイブ内のトップを bunashimeji/ に揃える。
( cd "${DIST_DIR}" && zip -rq "${ZIP_NAME}" bunashimeji )

echo "created: ${DIST_DIR}/${ZIP_NAME}"
