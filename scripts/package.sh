#!/usr/bin/env bash
# package.sh —— 打包免安装版：面板 + 网关，一个启动脚本同时拉起两者。
#
# 用法：
#   scripts/package.sh [网关源码目录]
#
#   网关源码目录缺省时自动克隆 $GATEWAY_REPO（默认 linbeize/workbuddy2api）。
#   TARGETS="linux/amd64 darwin/arm64" scripts/package.sh   # 只打指定平台
#   RELEASE_DIR=/tmp/out scripts/package.sh                  # 输出到别处（默认 ./release）
#
# 注意：每个平台的输出目录会先被删除重建。若在 release/ 下解压运行过某个包
# （里面有账号凭证），请用 RELEASE_DIR 输出到别处，避免覆盖。
#
# 产物（release/）：
#   wbgui-windows-<arch>.zip        解压后双击 start.bat
#   wbgui-linux-<arch>.tar.gz       解压后 ./start.sh
#   wbgui-darwin-<arch>.tar.gz      解压后 ./start.sh（macOS）
#
# 包内结构：
#   wbgui[.exe]  config.json  start.{bat,sh}  README.txt
#   gateway/workbuddy2api[.exe]  gateway/config.json  gateway/auths/  gateway/data/
#
# 面板以托管模式（gateway_exec）运行网关：随面板启动、崩溃自动拉起、面板退出时一并停止。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
RELEASE="${RELEASE_DIR:-$ROOT/release}"
TARGETS="${TARGETS:-windows/amd64 windows/arm64 linux/amd64 linux/arm64 darwin/amd64 darwin/arm64}"
GATEWAY_REPO="${GATEWAY_REPO:-https://github.com/linbeize/workbuddy2api}"
VERSION="$(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null || echo dev)"

GW_SRC="${1:-}"
if [[ -z "$GW_SRC" ]]; then
  GW_SRC="$(mktemp -d)/workbuddy2api"
  echo "==> 克隆网关源码 $GATEWAY_REPO"
  git clone --depth 1 "$GATEWAY_REPO" "$GW_SRC"
fi
GW_SRC="$(cd "$GW_SRC" && pwd)"
GW_VERSION="$(git -C "$GW_SRC" rev-parse --short HEAD 2>/dev/null || echo unknown)"

echo "==> 构建前端"
(cd "$ROOT/web" && (npm ci --no-audit --no-fund || npm install --no-audit --no-fund) && npm run build)
# 前端构建会改写入库的 dist/index.html 占位文件（资源哈希），打完包还原，避免污染工作区。
trap 'git -C "$ROOT" checkout -- internal/webui/dist/index.html 2>/dev/null || true' EXIT

# 文本文件换行：Windows 用 CRLF，避免记事本/cmd 显示异常。
eol() { if [[ "$1" == windows ]]; then sed 's/$/\r/'; else cat; fi; }

package_one() {
  local os="$1" arch="$2"
  local name="wbgui-${os}-${arch}"
  local out="$RELEASE/$name"
  local ext="" archive
  [[ "$os" == windows ]] && ext=".exe"
  if [[ "$os" == windows ]]; then archive="$name.zip"; else archive="$name.tar.gz"; fi

  echo "==> [$os/$arch] 编译面板 $VERSION 与网关 $GW_VERSION"
  rm -rf "$out" "$RELEASE/$archive"
  mkdir -p "$out/gateway/auths" "$out/gateway/data" "$out/data"
  (cd "$ROOT" && GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X main.version=$VERSION" -o "$out/wbgui$ext" ./cmd/server)
  (cd "$GW_SRC" && GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w" -o "$out/gateway/workbuddy2api$ext" ./cmd/server)
  # 凭证目录只给当前用户（auths 里是账号 token）。
  chmod 700 "$out/gateway/auths" "$out/data"

  # 网关配置：以上游 config.example.json 为模板，只监听本机，每个包随机生成 api_key。
  local api_key="sk-$(openssl rand -hex 24)"
  python3 - "$GW_SRC/config.example.json" "$out/gateway/config.json" "$api_key" <<'PY'
import json, sys
src, dst, key = sys.argv[1:4]
cfg = json.load(open(src, encoding="utf-8"))
cfg["listen"] = "127.0.0.1:7863"
cfg["api_key"] = key
cfg["auth_dir"] = "./auths"
cfg["state_file"] = "./data/state.json"
cfg.setdefault("server", {})["metrics_file"] = "./data/metrics.json"
json.dump(cfg, open(dst, "w", encoding="utf-8"), ensure_ascii=False, indent=2)
PY
  chmod 600 "$out/gateway/config.json"

  # 面板配置：相对路径以面板所在目录为基准（启动脚本会先 cd 过去）。
  eol "$os" > "$out/config.json" <<JSON
{
  "listen": "127.0.0.1:8787",
  "gateway_url": "http://127.0.0.1:7863",
  "gateway_api_key": "",
  "gateway_exec": "./gateway/workbuddy2api$ext",
  "auth_dir": "./gateway/auths",
  "auth_owner_uid": -1,
  "auth_owner_gid": -1,
  "config_file": "./gateway/config.json",
  "backup_dir": "./data/backups",
  "credentials_file": "./data/credentials.json",
  "pricing_file": "./data/pricing.json",
  "allowed_origins": [],
  "docker_container": "",
  "dangerous_ops": true,
  "read_only": false,
  "refresh_config_on_login": true,
  "timeout_seconds": 120,
  "ui": {
    "username": "admin",
    "password": "workbuddy",
    "session_ttl": "12h"
  }
}
JSON

  local start stop_hint sep
  if [[ "$os" == windows ]]; then
    start="双击 start.bat"
    stop_hint="关闭黑色命令行窗口即可"
    sep='\'
    eol "$os" > "$out/start.bat" <<'BAT'
@echo off
chcp 65001 >nul
cd /d "%~dp0"
title WorkBuddy2API
echo 面板地址: http://127.0.0.1:8787   网关地址: http://127.0.0.1:7863/v1
echo 关闭本窗口即同时停止面板与网关。
echo.
start "" /b cmd /c "timeout /t 4 /nobreak >nul & start "" http://127.0.0.1:8787"
wbgui.exe -config config.json
pause
BAT
  else
    start="在终端执行 ./start.sh"
    stop_hint="在终端按 Ctrl+C"
    sep='/'
    cat > "$out/start.sh" <<'SH'
#!/bin/sh
# 启动面板（面板会自动启动并托管网关）。Ctrl+C 停止两者。
cd "$(dirname "$0")" || exit 1
echo "面板地址: http://127.0.0.1:8787   网关地址: http://127.0.0.1:7863/v1"
echo "按 Ctrl+C 同时停止面板与网关。"
echo
# 几秒后尝试打开浏览器（无图形界面时静默跳过）。
( sleep 3
  if command -v open >/dev/null 2>&1; then open http://127.0.0.1:8787
  elif command -v xdg-open >/dev/null 2>&1; then xdg-open http://127.0.0.1:8787
  fi ) >/dev/null 2>&1 &
exec ./wbgui -config config.json
SH
    chmod +x "$out/start.sh"
  fi

  {
    cat <<TXT
WorkBuddy2API 面板 + 网关（免安装版，${os}/${arch}）
面板版本 ${VERSION} / 网关版本 ${GW_VERSION}

【启动】${start}。面板会自动启动网关，浏览器随后打开 http://127.0.0.1:8787
        默认登录：admin / workbuddy（登录后在「系统」页修改口令）
【停止】${stop_hint}，面板与网关会一起保存状态并退出。

【添加账号】面板「添加账号」页走网页授权；保存后面板会自动重启网关加载新账号。

【给客户端用的 API】
  Base URL : http://127.0.0.1:7863/v1
  API Key  : ${api_key}
  （保存在 gateway${sep}config.json 的 api_key，可在面板「网关配置」页修改，改后重启网关生效）

【目录说明】
  gateway${sep}auths${sep}   账号凭证（含 token，请勿外传）
  gateway${sep}data${sep}    网关账号池状态与请求统计
  data${sep}            面板口令、配置备份

【局域网访问】把 config.json 的 listen 改为 ":8787"（面板）、gateway${sep}config.json 的
  listen 改为 ":7863"（网关）。面板能读到全部账号 token，开放到网络前请务必改掉默认口令。

【高危操作】本版本默认开启 dangerous_ops，以便添加账号后自动重启网关、使用「重启网关」
  与「删除账号」。如只想监控，把 config.json 的 dangerous_ops 改为 false。
TXT
    if [[ "$os" == darwin ]]; then
      cat <<'TXT'

【macOS 提示「无法验证开发者」】本程序未经 Apple 签名。解压后在终端执行一次：
  xattr -dr com.apple.quarantine <解压出的目录>
  然后再运行 ./start.sh
TXT
    fi
    if [[ "$os" == linux ]]; then
      cat <<'TXT'

【后台常驻（可选）】用 systemd 管理时，ExecStart 指向本目录的 wbgui，
  WorkingDirectory 设为本目录，参数 -config config.json 即可。
TXT
    fi
  } | eol "$os" > "$out/README.txt"

  (cd "$RELEASE" && if [[ "$os" == windows ]]; then zip -qr "$archive" "$name"; else tar -czf "$archive" "$name"; fi)
  echo "    → $RELEASE/$archive"
}

mkdir -p "$RELEASE"
for t in $TARGETS; do
  package_one "${t%/*}" "${t#*/}"
done
echo "完成。"
