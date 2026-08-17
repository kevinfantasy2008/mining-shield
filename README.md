# Mining Shield

加密货币挖矿流量的加密中转隧道。在矿机与矿池之间加一跳加密代理，解决 Stratum 明文协议被窃听、劫持（如中间人替换钱包地址）、区域性封锁的问题。

## 架构

```
矿机 ──stratum+tcp──> 本地端 agent ══wss(443)══> Nginx ──> 服务器端 server ──stratum+tcp/ssl──> 矿池
      (内网明文)                 ↑ 加密隧道，流量特征         (伪装站点          (VPS，127.0.0.1)
                                 与普通 HTTPS 一致           + token 认证)
```

- **矿机零改造**：矿机仍填标准 `stratum+tcp://` 地址，只是指向本地端；
- **抗审查**：隧道走 wss/443 + 正规 CA 证书，服务器 443 同时挂伪装网站，未认证探测一律 404；可选套 CDN 隐藏 VPS 真实 IP；
- **字节级透传**：Stratum 会话由矿机与矿池直接协商，中转层不解析协议，天然兼容各币种与固件；
- **多路复用**：一条隧道承载多台矿机（StreamID 复用），断线指数退避重连，矿池侧多地址 failover。

## 快速部署

### 服务器端（公网 VPS）

```bash
curl -fsSL https://raw.githubusercontent.com/<owner>/mining-shield/main/scripts/install-server.sh | sudo bash
```

脚本会安装二进制、生成随机 `path`/`token` 并注册 systemd 服务。然后：

1. 编辑 `/etc/mining-shield/server.yaml`，填入真实矿池地址；
2. 配置 Nginx（参考 [deploy/nginx.conf](deploy/nginx.conf)）：443 端口 + Let's Encrypt 证书 + 伪装站点 + 秘密路径反代；
3. `systemctl start mining-shield-server`。

### 本地端（矿场 Linux 机器）

```bash
curl -fsSL https://raw.githubusercontent.com/<owner>/mining-shield/main/scripts/install-agent.sh | sudo bash
```

然后编辑 `/etc/mining-shield/agent.yaml`（url/token 与服务器端一致），`systemctl start mining-shield-agent`。

### 矿机配置

矿池地址填本地端的地址，例如：

```
stratum+tcp://192.168.1.10:3333
```

> 注意：矿机的备用矿池地址不要再填真实矿池的明文地址，否则隧道故障时矿机会绕过加密直连。

## 手动构建

需要 Go 1.21+：

```bash
# 本机
go build -o mining-shield-agent ./cmd/agent
go build -o mining-shield-server ./cmd/server

# 交叉编译（Linux 部署）
GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o mining-shield-agent-linux-amd64 ./cmd/agent
GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "-s -w" -o mining-shield-agent-linux-arm64 ./cmd/agent
GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o mining-shield-server-linux-amd64 ./cmd/server
```

## 配置说明

见 [configs/agent.example.yaml](configs/agent.example.yaml) 与 [configs/server.example.yaml](configs/server.example.yaml)，每个字段都有注释。

## 项目结构

```
cmd/agent/         本地端入口
cmd/server/        服务器端入口
internal/proto/    隧道帧协议（Type + StreamID + Payload，承载于 WS binary frame）
internal/tunnel/   Stream 多路复用（单写者、读写泵、优雅关闭）
internal/agent/    矿机监听、wss 客户端、断线重连
internal/server/   wss 接入、token 认证、矿池 failover
configs/           配置示例
deploy/            Nginx 反代配置
scripts/           一键安装脚本
```

## 安全说明

- 认证 token 在 TLS 内传输，外部不可见；服务器端常量时间比较；
- 生成强 token：`openssl rand -hex 32`；
- 建议 VPS 防火墙只放行 80/443，隧道服务只监听 127.0.0.1；
- 套 CDN（如 Cloudflare）可进一步隐藏 VPS IP，注意开启 WebSocket 支持。

## 路线图

- [ ] 帧填充（padding）与心跳抖动，抗流量行为分析
- [ ] 矿机在线状态 / share 统计（轻解析 `mining.submit`）
- [ ] Prometheus metrics 与简单 Web 面板
- [ ] 多隧道并发 + 负载均衡
