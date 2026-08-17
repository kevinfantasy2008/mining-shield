#!/usr/bin/env bash
# mining-shield 本地端（agent）一键安装脚本
# 用法（在矿场侧 Linux 机器上执行）：
#   curl -fsSL https://raw.githubusercontent.com/<owner>/mining-shield/main/scripts/install-agent.sh | sudo bash
# 或本地执行：
#   sudo bash scripts/install-agent.sh
#
# 可用环境变量覆盖默认值：
#   MINING_SHIELD_REPO  GitHub 仓库（默认 <owner>/mining-shield）
#   INSTALL_DIR         二进制安装目录（默认 /usr/local/bin）
#   CONFIG_DIR          配置目录（默认 /etc/mining-shield）
set -euo pipefail

REPO="${MINING_SHIELD_REPO:-<owner>/mining-shield}"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
CONFIG_DIR="${CONFIG_DIR:-/etc/mining-shield}"
BIN_NAME="mining-shield-agent"

if [ "$(id -u)" -ne 0 ]; then
    echo "请用 root 运行：sudo bash $0" >&2
    exit 1
fi

ARCH="$(uname -m)"
case "$ARCH" in
    x86_64)  GOARCH=amd64 ;;
    aarch64|arm64) GOARCH=arm64 ;;
    armv7l)  GOARCH=arm ;;
    *) echo "不支持的架构: $ARCH" >&2; exit 1 ;;
esac
echo "==> 架构: linux/$GOARCH"

install_from_release() {
    local url="https://github.com/${REPO}/releases/latest/download/${BIN_NAME}-linux-${GOARCH}"
    echo "==> 从 GitHub Release 下载: $url"
    curl -fsSL "$url" -o "${INSTALL_DIR}/${BIN_NAME}"
}

install_from_source() {
    echo "==> Release 下载失败，尝试从源码构建"
    if ! command -v go >/dev/null 2>&1; then
        echo "未安装 Go，无法从源码构建。请检查 Release 地址或手动安装 Go。" >&2
        exit 1
    fi
    local tmp
    tmp="$(mktemp -d)"
    trap 'rm -rf "$tmp"' EXIT
    git clone --depth 1 "https://github.com/${REPO}.git" "$tmp/mining-shield"
    (cd "$tmp/mining-shield" && go build -trimpath -ldflags "-s -w" -o "${INSTALL_DIR}/${BIN_NAME}" ./cmd/agent)
}

mkdir -p "$INSTALL_DIR" "$CONFIG_DIR"
install_from_release || install_from_source
chmod +x "${INSTALL_DIR}/${BIN_NAME}"

if [ ! -f "${CONFIG_DIR}/agent.yaml" ]; then
    echo "==> 生成配置模板 ${CONFIG_DIR}/agent.yaml"
    cat > "${CONFIG_DIR}/agent.yaml" <<'EOF'
listen: "0.0.0.0:3333"
servers:
  - url: "wss://your-domain.com/CHANGE-ME-random-path"
    token: "CHANGE-ME-long-random-token-at-least-32-chars"
ping_interval: "25s"
read_timeout: "90s"
min_backoff: "1s"
max_backoff: "30s"
EOF
    chmod 600 "${CONFIG_DIR}/agent.yaml"
fi

echo "==> 安装 systemd 服务"
cat > /etc/systemd/system/mining-shield-agent.service <<EOF
[Unit]
Description=Mining Shield Agent (miner-side stratum tunnel)
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=${INSTALL_DIR}/${BIN_NAME} -config ${CONFIG_DIR}/agent.yaml
Restart=always
RestartSec=3
NoNewPrivileges=true
ProtectSystem=full
ProtectHome=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable mining-shield-agent

cat <<MSG

安装完成。下一步：
  1. 编辑配置：  vi ${CONFIG_DIR}/agent.yaml
     - servers[].url   填你的 wss 隧道地址（与服务器端 path 一致）
     - servers[].token 填与服务器端一致的 token
  2. 启动服务：  systemctl start mining-shield-agent
  3. 查看日志：  journalctl -u mining-shield-agent -f
  4. 矿机矿池地址填：stratum+tcp://<本机内网IP>:3333
MSG
