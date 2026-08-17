#!/usr/bin/env bash
# mining-shield 服务器端（server）一键安装脚本
# 用法（在公网 VPS 上执行）：
#   GitHub:  curl -fsSL https://raw.githubusercontent.com/kevinfantasy2008/mining-shield/main/scripts/install-server.sh | sudo bash
#   Gitee:   curl -fsSL https://gitee.com/kevin-fantasy-2024/mining-shield/raw/main/scripts/install-server.sh | sudo MINING_SHIELD_HOST=gitee.com bash
#
# 可用环境变量覆盖默认值：
#   MINING_SHIELD_REPO  仓库路径（默认 kevinfantasy2008/mining-shield，安装后请改成实际用户名）
#   MINING_SHIELD_HOST  github.com（默认）或 gitee.com（国内网络推荐）
#   INSTALL_DIR         二进制安装目录（默认 /usr/local/bin）
#   CONFIG_DIR          配置目录（默认 /etc/mining-shield）
set -euo pipefail

REPO="${MINING_SHIELD_REPO:-kevinfantasy2008/mining-shield}"
HOST="${MINING_SHIELD_HOST:-github.com}"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
CONFIG_DIR="${CONFIG_DIR:-/etc/mining-shield}"
BIN_NAME="mining-shield-server"

case "$HOST" in
    github.com) CLONE_URL="https://github.com/${REPO}.git" ;;
    gitee.com)  CLONE_URL="https://gitee.com/${REPO}.git" ;;
    *) echo "MINING_SHIELD_HOST 只支持 github.com 或 gitee.com" >&2; exit 1 ;;
esac

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
    # Gitee 没有 latest/download 直链，会失败并自动走源码构建
    local url="https://${HOST}/${REPO}/releases/latest/download/${BIN_NAME}-linux-${GOARCH}"
    echo "==> 尝试从 Release 下载: $url"
    curl -fsSL "$url" -o "${INSTALL_DIR}/${BIN_NAME}"
}

install_from_source() {
    echo "==> Release 下载失败，转为从源码构建"
    if ! command -v go >/dev/null 2>&1; then
        echo "未安装 Go，无法从源码构建。请先安装 Go（国内可用 https://golang.google.cn/dl/）。" >&2
        exit 1
    fi
    # 国内网络走 goproxy.cn 加速依赖下载
    if [ "$HOST" = "gitee.com" ] && [ -z "${GOPROXY:-}" ]; then
        export GOPROXY="https://goproxy.cn,direct"
    fi
    local tmp
    tmp="$(mktemp -d)"
    trap 'rm -rf "$tmp"' EXIT
    git clone --depth 1 "$CLONE_URL" "$tmp/mining-shield"
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
# 多币种命名路由（键与本地端 listeners[].route 对应）：
# routes:
#   kas:
#     - "stratum+tcp://kas.example-pool.com:16110"
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

# ============ Nginx 反代（443 入口 + 伪装站点） ============
# 即使走 Cloudflare 代理也需要 Nginx：CF 只负责边缘终止 TLS 和隐藏 IP，
# 源站上的伪装站点、秘密路径分发、token 双重校验都由 Nginx 承担。

if ! command -v nginx >/dev/null 2>&1; then
    echo "==> 安装 Nginx"
    if command -v apt-get >/dev/null 2>&1; then
        apt-get update -qq && apt-get install -y -qq nginx
    elif command -v dnf >/dev/null 2>&1; then
        dnf install -y -q nginx
    elif command -v yum >/dev/null 2>&1; then
        yum install -y -q nginx
    else
        echo "无法自动安装 Nginx（未识别 apt/dnf/yum），请手动安装后重跑本脚本" >&2
        exit 1
    fi
fi

# 从 server.yaml 提取 path/token/listen，注入 Nginx 配置
YAML_PATH="$(grep '^path:' "${CONFIG_DIR}/server.yaml" | sed 's/^path: *"//; s/" *$//')"
YAML_TOKEN="$(grep '^token:' "${CONFIG_DIR}/server.yaml" | sed 's/^token: *"//; s/" *$//')"
YAML_LISTEN="$(grep '^listen:' "${CONFIG_DIR}/server.yaml" | sed 's/^listen: *"//; s/" *$//')"
DOMAIN="${DOMAIN:-your-domain.com}"

echo "==> 写入 Nginx 配置 /etc/nginx/conf.d/mining-shield.conf"
cat > /etc/nginx/conf.d/mining-shield.conf <<EOF
# mining-shield 隧道反代（由 install-server.sh 生成）
# 用 Cloudflare 代理时：SSL 模式选 Full (Strict)，证书用 CF 的 Origin Certificate
server {
    listen 443 ssl;
    server_name ${DOMAIN};

    # Cloudflare 面板 → SSL/TLS → Origin Server → Create Certificate，粘贴到这两个文件
    ssl_certificate     /etc/nginx/ssl/mining-shield.pem;
    ssl_certificate_key /etc/nginx/ssl/mining-shield.key;
    ssl_protocols       TLSv1.2 TLSv1.3;

    # 伪装站点：放一个真实可访问的网站（主动探测者看到的是正常站点）
    root /var/www/html;
    index index.html;

    location / {
        try_files \$uri \$uri/ =404;
    }

    # 隧道入口（路径和 token 与 server.yaml 自动同步）
    location ${YAML_PATH} {
        if (\$http_x_auth_token != "${YAML_TOKEN}") { return 404; }
        proxy_pass http://${YAML_LISTEN};
        proxy_http_version 1.1;
        proxy_set_header Upgrade \$http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host \$host;
        proxy_set_header X-Auth-Token \$http_x_auth_token;
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
        proxy_buffering off;
    }
}
EOF

# 伪装站点占位页（建议换成真实网站内容）
mkdir -p /var/www/html /etc/nginx/ssl
if [ ! -f /var/www/html/index.html ]; then
    echo '<!doctype html><html><head><title>Welcome</title></head><body><h1>Welcome</h1></body></html>' > /var/www/html/index.html
fi

if [ -f /etc/nginx/ssl/mining-shield.pem ]; then
    nginx -t && systemctl enable nginx && systemctl restart nginx
    echo "==> Nginx 已启动"
else
    echo "==> 证书还未安装，Nginx 暂不启动（见下方说明）"
    systemctl enable nginx 2>/dev/null || true
fi

cat <<MSG

安装完成。下一步：

  【证书】Cloudflare 代理模式下：
     1. CF 面板 → SSL/TLS → 加密模式选 Full (Strict)
     2. CF 面板 → SSL/TLS → Origin Server → Create Certificate（免费，15 年）
     3. 把证书粘贴到 /etc/nginx/ssl/mining-shield.pem
        把私钥粘贴到 /etc/nginx/ssl/mining-shield.key
        chmod 600 /etc/nginx/ssl/mining-shield.key
     4. nginx -t && systemctl restart nginx

  【域名】编辑 /etc/nginx/conf.d/mining-shield.conf，
     把 server_name 的 ${DOMAIN} 改成你的真实域名（或用 DOMAIN=xxx 重跑脚本）

  【防火墙】只放行 Cloudflare IP 段访问 80/443，防止绕过 CF 直连源站：
     curl -s https://www.cloudflare.com/ips-v4 | while read ip; do
       ufw allow from \$ip to any port 443 proto tcp
     done

  【服务】
     1. 编辑配置：vi ${CONFIG_DIR}/server.yaml（pools 填真实矿池；path/token 已随机生成）
     2. 启动：     systemctl start mining-shield-server
     3. 日志：     journalctl -u mining-shield-server -f
     4. 本地端 agent.yaml 的 url 用 wss://你的域名 + path，token 与 server.yaml 一致
MSG
