#!/usr/bin/env bash
# mining-shield 服务器端（server）一键安装脚本
# 用法（在公网 VPS 上执行）：
#   curl -fsSL https://raw.githubusercontent.com/<owner>/mining-shield/main/scripts/install-server.sh | sudo bash
#
# 可用环境变量覆盖默认值：
#   MINING_SHIELD_REPO  GitHub 仓库（默认 <owner>/mining-shield）
#   INSTALL_DIR         二进制安装目录（默认 /usr/local/bin）
#   CONFIG_DIR          配置目录（默认 /etc/mining-shield）
set -euo pipefail

REPO="${MINING_SHIELD_REPO:-<owner>/mining-shield}"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
CONFIG_DIR="${CONFIG_DIR:-/etc/mining-shield}"
BIN_NAME="mining-shield-server"

if [ "$(id -u)" -ne 0 ]; then
    echo "请用 root 运行：sudo bash $0" >&2
    exit 1
fi

ARCH="$(uname -m)"
case "$ARCH" in
    x86_64)  GOARCH=amd64 ;;
    aarch64|arm64) GOARCH=arm64 ;;
    *) echo "不支持的架构: $ARCH（VPS 一般是 x86_64 或 aarch64）" >&2; exit 1 ;;
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
    (cd "$tmp/mining-shield" && go build -trimpath -ldflags "-s -w" -o "${INSTALL_DIR}/${BIN_NAME}" ./cmd/server)
}

mkdir -p "$INSTALL_DIR" "$CONFIG_DIR"
install_from_release || install_from_source
chmod +x "${INSTALL_DIR}/${BIN_NAME}"

if [ ! -f "${CONFIG_DIR}/server.yaml" ]; then
    echo "==> 生成配置模板 ${CONFIG_DIR}/server.yaml"
    TOKEN="$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')"
    SECRET_PATH="/$(head -c 8 /dev/urandom | od -An -tx1 | tr -d ' \n')"
    cat > "${CONFIG_DIR}/server.yaml" <<EOF
listen: "127.0.0.1:8080"
path: "${SECRET_PATH}"
token: "${TOKEN}"
pools:
  - "stratum+ssl://btc.example-pool.com:443"
  - "stratum+tcp://btc.example-pool.com:3333"
dial_timeout: "10s"
read_timeout: "90s"
EOF
    chmod 600 "${CONFIG_DIR}/server.yaml"
    echo "==> 已自动生成随机 path 和 token（见配置文件）"
fi

echo "==> 安装 systemd 服务"
cat > /etc/systemd/system/mining-shield-server.service <<EOF
[Unit]
Description=Mining Shield Server (pool-side stratum tunnel)
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=${INSTALL_DIR}/${BIN_NAME} -config ${CONFIG_DIR}/server.yaml
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
systemctl enable mining-shield-server

cat <<MSG

安装完成。下一步：
  1. 编辑配置：      vi ${CONFIG_DIR}/server.yaml
     - pools 填真实矿池地址；path/token 已自动生成随机值
  2. 启动服务：      systemctl start mining-shield-server
  3. 查看日志：      journalctl -u mining-shield-server -f
  4. 配置 Nginx：    参考 deploy/nginx.conf，把 location 路径和 token
                     改成与 server.yaml 一致，挂上伪装站点和证书
  5. 本地端 agent.yaml 的 url/token 与 server.yaml 保持一致
MSG
