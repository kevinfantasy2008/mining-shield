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
- **多币种多矿池**：本地端多个监听端口各绑定路由，服务器端按路由分发到对应矿池组；
- **多路复用**：一条隧道承载多台矿机（StreamID 复用），断线指数退避重连，矿池侧多地址 failover。

## 多币种路由

本地端用 `listeners` 给不同币种开不同端口，服务器端用 `routes` 配各自的矿池组：

```yaml
# agent.yaml（本地端）
listeners:
  - listen: "0.0.0.0:3333"        # 默认路由 → 服务器端 pools
  - listen: "0.0.0.0:4333"
    route: "kas"                  # KAS 矿机连 4333
  - listen: "0.0.0.0:5333"
    route: "ltc"                  # LTC 矿机连 5333
```

```yaml
# server.yaml（服务器端）
pools:                            # 默认路由（如 BTC）
  - "stratum+ssl://btc.example-pool.com:443"
routes:
  kas:
    - "stratum+tcp://kas.example-pool.com:16110"
  ltc:
    - "stratum+ssl://ltc.example-pool.com:50505"
```

所有币种共享同一条加密隧道；每个矿池组内仍可配多个地址做 failover。

## 快速部署

### 服务器端（公网 VPS）

```bash
curl -fsSL https://raw.githubusercontent.com/kevinfantasy2008/mining-shield/main/scripts/install-server.sh | sudo bash

# 可选：直接指定域名（写入 Nginx 配置）
curl -fsSL ... | sudo DOMAIN=your-domain.com bash
```

脚本一次完成：安装二进制 → 生成随机 `path`/`token` → 注册 systemd 服务 → **安装 Nginx 并生成反代配置**（`/etc/nginx/conf.d/mining-shield.conf`，路径和 token 自动与 server.yaml 同步）。之后只需：

1. 编辑 `/etc/mining-shield/server.yaml`，填入真实矿池地址；
2. 安装证书（推荐 Cloudflare Origin Certificate，见下节）；
3. `systemctl start mining-shield-server && systemctl restart nginx`。

### 本地端（矿场 Linux 机器）

```bash
curl -fsSL https://raw.githubusercontent.com/kevinfantasy2008/mining-shield/main/scripts/install-agent.sh | sudo bash
```

然后编辑 `/etc/mining-shield/agent.yaml`（url/token 与服务器端一致），`systemctl start mining-shield-agent`。

### 国内网络：Gitee 镜像

如果目标机器访问 GitHub 困难，使用 Gitee 镜像仓库：

```bash
# 本地端
curl -fsSL https://gitee.com/kevin-fantasy-2024/mining-shield/raw/main/scripts/install-agent.sh | \
  sudo MINING_SHIELD_HOST=gitee.com MINING_SHIELD_REPO=kevin-fantasy-2024/mining-shield bash

# 服务器端
curl -fsSL https://gitee.com/kevin-fantasy-2024/mining-shield/raw/main/scripts/install-server.sh | \
  sudo MINING_SHIELD_HOST=gitee.com MINING_SHIELD_REPO=kevin-fantasy-2024/mining-shield bash
```

Gitee 没有 Release 直链，脚本会自动转为**从源码构建**（需要目标机器装有 Go，依赖走 goproxy.cn 国内加速）。

仓库同步：Gitee 网页端「新建仓库 → 导入已有仓库」填入 GitHub 地址即可一键导入；之后在 Gitee 仓库「管理」页可开启自动同步，或用 `git push gitee main` 手动推送。

### 矿机配置

矿池地址填本地端的地址，例如：

```
stratum+tcp://192.168.1.10:3333
```

> 注意：矿机的备用矿池地址不要再填真实矿池的明文地址，否则隧道故障时矿机会绕过加密直连。

## Cloudflare 代理模式（推荐）

### 开了 CF 代理还需要 Nginx 吗？—— 需要

这是最常见的疑问。CF 代理和 Nginx 是**两层不同的东西**，缺一不可：

```
矿机 → 本地端 agent → [Cloudflare 边缘] → [Nginx on VPS] → mining-shield-server → 矿池
                          ↑ 这层做的事            ↑ 这层做的事
                     终止 TLS（对客户端）      伪装站点（抗主动探测）
                     隐藏 VPS 真实 IP         秘密路径分发
                     抗 DDoS / IP 信誉        token 双重校验 → 反代到 127.0.0.1:8080
```

- CF 是「边缘」，只把流量转发到你的 VPS；它不提供源站上的网站和反代逻辑；
- Nginx 是「源站入口」，没有它，443 端口上要么什么都没有（探测即暴露），要么得让隧道服务自己直接监听 443（失去伪装站点和路径分流）。

### CF 面板设置清单

| 位置 | 设置 | 值 |
|---|---|---|
| DNS → A 记录 | 代理状态 | **已代理（橙色云）** |
| SSL/TLS → 概述 | 加密模式 | **Full (Strict)**（切勿 Flexible） |
| SSL/TLS → Origin Server | 源站证书 | Create Certificate（见下方手动步骤） |
| Network | WebSockets | On（默认即开） |
| SSL/TLS → 边缘证书 | Always Use HTTPS | 建议 On |

### 源站证书（CF Origin Certificate，需手动创建一次）

这两个文件**无法由安装脚本自动生成**——证书绑定你的 Cloudflare 账号和域名，必须登录 CF 面板生成后手动粘贴到 VPS。只需做一次（有效期 15 年）：

1. CF 面板 → 选你的域名 → **SSL/TLS → Origin Server** → **Create Certificate**
2. 保持默认（RSA 2048、15 年、主机名已自动包含域名）→ Create
3. 复制页面显示的两段文本，粘贴到 VPS（⚠️ 私钥只显示这一次）：

```bash
vi /etc/nginx/ssl/mining-shield.pem   # 粘贴 Origin Certificate（BEGIN CERTIFICATE 那段）
vi /etc/nginx/ssl/mining-shield.key   # 粘贴 Private Key（BEGIN PRIVATE KEY 那段）
chmod 600 /etc/nginx/ssl/mining-shield.key
nginx -t && systemctl restart nginx
```

> 不用 CF 代理（VPS 直连模式）时跳过此节，改用 Let's Encrypt：`certbot --nginx -d 你的域名` 一条命令自动完成。

### 源站防火墙（关键）

开代理后务必锁死源站，否则别人扫到 VPS IP 就能绕过 CF 直连，代理形同虚设。

**AWS 用户（推荐做法，无需登录 VPS、无需 root）**：直接在安全组配置——EC2 控制台 → 实例 → 安全 → 安全组 → 编辑入站规则，443 端口只允许 Cloudflare IP 段（共 15 条 CIDR，见 https://www.cloudflare.com/ips/ ），删除 `0.0.0.0/0` 放行的规则，SSH(22) 限制为你自己的 IP。Lightsail 在实例的「联网 → 防火墙」里同样配置。配好后 VPS 上不需要再动 ufw。

**其他 VPS**：

```bash
# 只放行 Cloudflare IP 段访问 443（SSH 端口按需另行保留）
curl -s https://www.cloudflare.com/ips-v4 | while read ip; do
  ufw allow from $ip to any port 443 proto tcp
done
```

### 为什么隧道不会被 CF 断链

CF 免费版 WebSocket 空闲超时约 100 秒；本地端 `ping_interval: 25s` 的心跳远小于此，长连接天然保活，无需任何改动。

### 信任边界提醒

开 CF 代理意味着 Cloudflare 作为中间人能解密看到你的隧道流量（token、矿工地址等）。这是 CDN 隐藏源站 IP 的固有代价。如果你的首要威胁是「按 IP/域名封锁」，这个交换划算；如果首要威胁是「流量内容保密性极端敏感」，则不开 CF 代理、直连 VPS（此时 Nginx 上用 Let's Encrypt 证书，`deploy/nginx.conf` 里的配置即为直连模式写法）。

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

- [x] 多币种多矿池路由（v0.2.0）
- [ ] 帧填充（padding）与心跳抖动，抗流量行为分析
- [ ] 矿机在线状态 / share 统计（轻解析 `mining.submit`）
- [ ] Prometheus metrics 与简单 Web 面板
- [ ] 多隧道并发 + 负载均衡
