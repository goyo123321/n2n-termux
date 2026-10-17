# n2n-termux

**n2n 组网系统的 Termux 客户端** —— 基于 SOCKS5 代理，无需 root。

配合 [edge-signal](https://github.com/goyo123321/edge-signal) 使用，通过 Cloudflare Workers 信令服务器建立 **LAN 直连 / P2P / TURN / WebSocket 中继** 四级降级连接。

**与 [n2n-go-client](https://github.com/goyo123321/n2n-go-client)、[n2n-android](https://github.com/goyo123321/n2n-android) 完全互通** —— Termux 通过 SOCKS5 访问其他节点的服务，PC / Android 也能主动访问 Termux 上的 TCP / UDP / ICMP 服务。

---

## 版本

当前版本：**v1.1.18**

---

## 🌐 四端协同全景

```
              ┌───────────────────────────────────────────┐
              │      edge-signal (Cloudflare Worker)      │
              │  ├─ 信令路由                              │
              │  ├─ NAT 打洞协调                          │
              │  ├─ TURN 凭证下发                         │
              │  └─ WS 中继（最后兜底）                   │
              └──────────────┬────────────────────────────┘
                             │
         ┌───────────────────┼───────────────────┐
         │                   │                   │
         ▼                   ▼                   ▼
   n2n-go-client       n2n-termux          n2n-android
   ┌──────────┐       ┌──────────┐        ┌──────────┐
   │ TUN 模式  │       │ SOCKS5   │        │ VpnService│
   │ 系统级路由│       │ 代理模式 │        │ 无 root  │
   │ 需 root  │       │ 无 root  │        │          │
   └──────────┘       └──────────┘        └──────────┘
     Linux/macOS         Android              Android
     Windows             Termux                APK
     路由器

       LAN / P2P / TURN / WS  --  完全互通
```

### 三端定位对比

| 维度 | n2n-go-client | **n2n-termux**（本仓库） | n2n-android |
|:---|:---|:---|:---|
| **运行环境** | Linux / macOS / Windows / 路由器 | Android Termux | Android APK |
| **数据面** | TUN 虚拟网卡 | SOCKS5 代理 | VpnService（TUN） |
| **权限要求** | root / 管理员 | **无 root** | 无 root |
| **协议支持（主动）** | TCP / UDP / ICMP | **仅 TCP**（SOCKS5 限制） | TCP / UDP / ICMP |
| **协议支持（被动）** | — | **TCP / UDP / ICMP**（packet-forwarder） | — |
| **应用透明** | ✅ 系统级 | ⚠️ 需 App 显式配 SOCKS5 | ✅ 系统级 |

---

## ✨ 特性

- **无需 root** — 纯 SOCKS5 模式，任何 Android 设备都能跑
- **双向互通** — PC / Android 可主动访问 Termux 上的 TCP / UDP / ICMP 服务
- **四级降级** — `LAN → P2P → TURN → WS`，连接永远可用
- **P2P 直连** — STUN 探测 + NAT 打洞，延迟低至 10ms
- **多目标打洞** — LAN + STUN 反射 + 观测 raddr 同时扫描，命中率大幅提升
- **CGNAT hairpin** — 同 STUN 出口 IP 时尝试端口扫描 + probe 回发
- **UDP 保活** — 每 5 秒刷新 STUN 映射，防止 CGNAT 端口漂移
- **P2P 通道保活** — 每 15 秒往对端发 N2NS PING 多目标探针
- **TURN 中继** — 支持 Cloudflare TURN + 自建 coturn（UDP / TCP / RFC6062）
- **中继回退** — TURN 失败自动降级到 WebSocket 中继
- **断线重连** — WebSocket 自动重连 + 90 秒读超时检测下行僵死
- **CA 证书自动检测** — Termux 上自动找到 CA 证书，无需手工设置 `SSL_CERT_FILE`
- **CLIENT_ID 持久化** — 重启后虚拟 IP 不变
- **虚拟网段可配** — 服务端通过 `VIRTUAL_NETWORK` 下发
- **零依赖** — 单个静态二进制，无需额外安装

---

## 🏗️ 架构

```
┌──────────────────────────────────────────┐
│           Termux 客户端                   │
│                                           │
│   ┌──────────┐   ┌──────────┐            │
│   │ App 请求  │──→│ SOCKS5   │            │
│   │ (curl等)  │   │ 127.0.0.1│            │
│   └──────────┘   └────┬─────┘            │
│                       │                   │
│                       ↓                   │
│                  ┌──────────┐            │
│                  │ 数据泵    │            │
│                  │ N2NS 帧   │            │
│                  └────┬─────┘            │
│                       │                   │
│   ┌──────────┐        │                   │
│   │ packet-  │←───────┘                   │
│   │ forwarder│   （PC/Android 主动访问）    │
│   │ TCP/UDP/ │                            │
│   │ ICMP     │                            │
│   └──────────┘                            │
│                       │                   │
│                       ↓                   │
│                  ┌──────────┐            │
│                  │ UDP/WS   │            │
│                  │ 传输层    │            │
│                  └──────────┘            │
└──────────────────────────────────────────┘
```

**数据面双向**：

- **主动访问**（Termux → PC/Android）：SOCKS5 入口 → N2NS 帧 → UDP/WS
- **被动访问**（PC/Android → Termux）：UDP/WS → packet-forwarder → 本地 `127.0.0.1:port`

---

## 📡 四级降级链路

```
优先级 1: LAN 直连        ← 同 WiFi 场景，零 NAT 穿越
   ↓ 不同 LAN
优先级 2: P2P 直连        ← 打洞成功，含 CGNAT hairpin
   ↓ 打洞失败
优先级 3: TURN 中继       ← 专业 UDP 中继，不消耗 Worker
   ↓ TURN 不可用/失败
优先级 4: WS 中继         ← 最后兜底，走 Worker，消耗配额
```

**每对 peer 独立决策**：A↔B 可能走 LAN，A↔C 走 P2P，A↔D 走 TURN，互不影响。

---

## 🔥 保活机制

| 层 | 间隔 | 目标 | 作用 |
|:---|:---|:---|:---|
| **UDP 保活** | 5 秒 | 3 个 STUN 服务器 | 保 socket 的 CGNAT 映射 |
| **P2P 通道保活** | 15 秒 | 每个 P2P 对端 | 保 A→B 会话 |

**流量代价**：约 50 MB/天（UDP 保活）+ 180 KB/天 × 对端数（P2P 保活）。真实数据帧天然刷新 CGNAT 映射，保活只在空闲时兜底。

---

## 🚀 快速开始（手动安装）

**不使用一键脚本**。整个过程只需 3 步。

### 1. 下载二进制

到 [Releases](https://github.com/goyo123321/n2n-termux/releases/tag/v1.1.18) 下载对应架构：

| 架构 | 文件名 | 适用设备 |
|:---|:---|:---|
| **ARM64** | `n2n-client-linux-arm64` | 绝大多数 Android 手机（2018 年后） |
| ARMv7 | `n2n-client-linux-armv7` | 老旧 32 位 Android |

**在 Termux 里直接下载**：

```bash
# ARM64
curl -LO https://github.com/goyo123321/n2n-termux/releases/download/v1.1.18/n2n-client-linux-arm64
```

**如果 GitHub 访问慢**，可以在电脑上下载后 `adb push` / `scp` / 局域网 HTTP 传到手机。

### 2. 给执行权限

```bash
# 创建目录
mkdir -p ~/.n2n-termux/bin
mv n2n-client-linux-arm64 ~/.n2n-termux/bin/n2n-client
chmod +x ~/.n2n-termux/bin/n2n-client

# 验证（应输出 ARM aarch64）
file ~/.n2n-termux/bin/n2n-client
```

### 3. 无交互运行

**前台运行**（Ctrl+C 停止）：

```bash
STUN_SERVERS="stun.chat.bilibili.com:3478,stun.hitv.com:3478" \
SIGNALING_URL="wss://url.jyece.kdns.fr" \
ROOM_ID="default-room" \
CLIENT_ID="termux-a" \
NODE_NAME="Termux-A" \
SOCKS5_LISTEN="127.0.0.1:1080" \
~/.n2n-termux/bin/n2n-client
```

**后台运行**（推荐）：

```bash
nohup env \
  STUN_SERVERS="stun.chat.bilibili.com:3478,stun.hitv.com:3478" \
  SIGNALING_URL="wss://url.jyece.kdns.fr" \
  ROOM_ID="default-room" \
  CLIENT_ID="termux-a" \
  NODE_NAME="Termux-A" \
  SOCKS5_LISTEN="127.0.0.1:1080" \
  ~/.n2n-termux/bin/n2n-client > ~/n2n.log 2>&1 &
```

**查看日志**：

```bash
tail -f ~/n2n.log
```

**停止**：

```bash
pkill -f n2n-client
```

---

## ⚙️ 配置项（环境变量）

| 变量 | 必填 | 默认值 | 说明 |
|:---|:---|:---|:---|
| `SIGNALING_URL` | ✅ | — | edge-signal 的 WSS 地址 |
| `CONNECT_TOKEN` | ⚠️ | `""` | 连接密码（Worker 配了才需要）|
| `ROOM_ID` | ❌ | `default-room` | 房间名。只允许 `[A-Za-z0-9_-]+` |
| `CLIENT_ID` | ❌ | 自动生成并持久化 | 客户端唯一 ID |
| `NODE_NAME` | ❌ | hostname | 显示名 |
| `SOCKS5_LISTEN` | ❌ | `127.0.0.1:1080` | SOCKS5 监听地址 |
| `STUN_SERVERS` | ❌ | 内置 5 个 | 逗号分隔的 STUN 服务器列表 |
| `UDP_PORT` | ❌ | `0` | P2P UDP 端口（`0`=内核分配）|

**虚拟网段**由服务端 `wrangler.toml` 的 `VIRTUAL_NETWORK` 决定，客户端从 `ready` 消息自动接收。

**修改配置后**需要重启客户端。

---

## 🌐 使用 SOCKS5

**必须显式走 SOCKS5** —— 因为 n2n-termux 只提供 `127.0.0.1:1080` 代理入口。

**curl**：

```bash
# 单条命令
curl --socks5 127.0.0.1:1080 http://10.64.0.3:8080/

# 环境变量（会话级）
export ALL_PROXY=socks5://127.0.0.1:1080
curl http://10.64.0.3:8080/
```

**ssh**（通过 ProxyCommand）：

```bash
ssh -o ProxyCommand='nc -X 5 -x 127.0.0.1:1080 %h %p' user@10.64.0.3
```

**wget**：

```bash
wget -e use_proxy=yes -e socks_proxy=127.0.0.1:1080 http://10.64.0.3:8080/
```

**Python**：

```python
import socks  # pip install PySocks
import socket
socks.set_default_proxy(socks.SOCKS5, "127.0.0.1", 1080)
socket.socket = socks.socksocket
```

---

## 🌐 典型使用场景

### 场景 A：Termux 访问 PC 上的服务

**PC 端起服务**（监听 `0.0.0.0`）：

```bash
# Linux/macOS
python3 -m http.server 8080 --bind 0.0.0.0

# Windows
python -m http.server 8080 --bind 0.0.0.0
```

**Termux 端通过 SOCKS5 访问**：

```bash
curl --socks5 127.0.0.1:1080 http://10.64.0.2:8080/
```

### 场景 B：PC 访问 Termux 上的服务

**Termux 端起服务**：

```bash
# 监听 0.0.0.0，否则 PC 访问不到
python3 -m http.server 8080 --bind 0.0.0.0
# 或
sshd    # SSH 服务监听 8022
```

**PC 端直接访问**（走 TUN）：

```bash
curl http://10.64.0.3:8080/
ssh -p 8022 u0_aXXX@10.64.0.3
```

**注意**：Termux 端服务**必须监听 `0.0.0.0`**，否则 PC 通过虚拟 IP 访问不到。

### 场景 C：Termux ↔ Termux

**主动方**：

```bash
curl --socks5 127.0.0.1:1080 http://10.64.0.4:8080/
```

**被动方**：

```bash
python3 -m http.server 8080 --bind 0.0.0.0
```

**注意**：两端都得跑 n2n-termux。

---

## 🔗 三端互通矩阵

| 主动方 | 被动方 | TCP | UDP | ICMP |
|:---|:---|:---|:---|:---|
| **PC → Termux** | ✅ | ✅ | ✅ |
| **Android → Termux** | ✅ | ✅ | ✅ |
| **PC → Android** | ✅ | ✅ | ✅ |
| **Android → PC** | ✅ | ✅ | ✅ |
| **Termux → PC/Android** | ✅ | ❌ | ❌ |
| **Termux → Termux** | ✅ | ❌ | ❌ |

**Termux 主动发 UDP/ICMP 做不到** —— 这是 SOCKS5 协议本身的限制，不是权限问题。

---

## 📋 启动日志

启动成功后：

```
n2n-termux v1.1.18 启动
[TLS] CA 证书: /data/data/com.termux/files/usr/etc/tls/cert.pem (xxx bytes)
[DNS] 使用 ... nameserver: [8.8.8.8:53 8.8.4.4:53]
[FWD] 已启动（TCP 中继 + UDP 转发 + ICMP 响应）
[P2P] UDP 监听端口 44145
[NAT] 启动预收集 LAN: [...]
[NAT] HardNAT（对称），ports_diff=3 pub=...
[WS] 连接 wss://...
已连接信令，Client ID: termux-a，节点名: Termux-A
分配虚拟 IP: 10.64.0.3（网段 10.64.0.0/24）
[信令] 服务端看到的本机出口 IP: ...
[NAT] 上报 LAN 地址: [...]
[信令] 上报 p2p_metadata: ...
[信令] ready: 返回 N 个已有节点
[TURN] 中继地址: ...
[Keepalive] 启动，每 5s 刷新 5 个 STUN 服务器
[P2P-Keepalive] 启动，每 15s 刷新 P2P 通道
[SOCKS5] 监听 127.0.0.1:1080

================= 本机信息 =================
  Client ID   : termux-a
  节点名       : Termux-A
  虚拟 IP     : 10.64.0.3
  虚拟网段    : 10.64.0.0/24
  P2P 端口    : 44145
  模式         : SOCKS5 + packet-forwarder（双向互通）
==========================================
```

**PC/Android 主动访问 Termux 时**：

```
[FWD-ICMP] Echo Request from 10.64.0.2
[FWD-TCP] + 10.64.0.2:xxxxx → 127.0.0.1:8080
[FWD-TCP] ✓ established 10.64.0.2:xxxxx
[FWD-UDP] + 10.64.0.2:xxxxx → 127.0.0.1:5353
```

---

## 🐛 常见问题

### Q: 无 root 真的能用吗？

**能**。Termux 本身不需要 root，SOCKS5 模式也不需要特殊权限。

**但要清楚限制**：
- 应用**必须**显式走 SOCKS5 代理（不能用 `ping` 主动发 ICMP，不能直接 `curl http://10.64.0.x`）
- 主动方向仅支持 TCP（不支持 UDP / ICMP）
- 被动方向（PC/Android 访问 Termux）支持 TCP / UDP / ICMP

### Q: 为什么 `ping 10.64.0.3` 不通？

**ICMP 不走 SOCKS5**。SOCKS5 是 TCP 代理协议，`ping` 用的是 ICMP，两者完全独立。

**用 TCP 测**：

```bash
# 对端开服务
python3 -m http.server 8080 --bind 0.0.0.0

# 本机测
curl --socks5 127.0.0.1:1080 http://10.64.0.3:8080/
```

### Q: 为什么 `curl http://10.64.0.3:8080/` 不通？

**没走 SOCKS5**。默认 `curl` 直连，会尝试通过系统网络访问 `10.64.0.3` —— 但该地址在系统里不存在。

**必须显式走 SOCKS5**：

```bash
curl --socks5 127.0.0.1:1080 http://10.64.0.3:8080/
```

### Q: PC 访问 Termux 服务失败？

**Termux 端服务必须监听 `0.0.0.0`**。

```bash
# ✅ 可以（PC 能访问）
python3 -m http.server 8080 --bind 0.0.0.0

# ❌ 不行（只监听 loopback，PC 访问不到）
python3 -m http.server 8080 --bind 127.0.0.1
```

### Q: TLS 证书错误 `x509: certificate signed by unknown authority`？

**Termux 缺少 CA 证书**。

```bash
pkg install ca-certificates
```

**v1.1.18 已内置自动检测**，安装证书后直接运行即可，无需设置 `SSL_CERT_FILE`。

### Q: 打洞一直失败？

看日志 `[NAT-HOLE] ❌ 失败`。会自动降级到 TURN 中继。TURN 也不可用则降级到 WS 中继。

**排查**：

1. **`publicEndpoint` 为空** → NAT 探测失败，STUN 服务器被封
2. **端口差 > 100** → CGNAT 映射漂移严重
3. **同 STUN 出口 IP** → 同 CGNAT，需要 hairpin

### Q: 手动指定 SOCKS5 端口

```bash
SOCKS5_LISTEN="127.0.0.1:1081" \
SIGNALING_URL="wss://..." \
~/.n2n-termux/bin/n2n-client
```

### Q: 怎么看日志？

```bash
# 前台运行时，直接看
~/.n2n-termux/bin/n2n-client ...

# 后台运行时
tail -f ~/n2n.log

# 或过滤关键字
tail -f ~/n2n.log | grep -E "\[P2P\]|\[NAT-HOLE\]|\[连接\]|\[FWD\]"
```

### Q: 怎么停止客户端？

```bash
# 方式 1：pkill
pkill -f n2n-client

# 方式 2：查 PID
ps aux | grep n2n-client
kill <pid>
```

### Q: 重启后虚拟 IP 变了？

客户端会持久化 `CLIENT_ID` 到 `~/.n2n-termux/client_id`，正常情况下重启 IP 不变。

如果 IP 变了，检查：

```bash
cat ~/.n2n-termux/client_id
```

文件被删或换目录会导致 IP 变。

### Q: 面板显示 `p2p-avail` 不是 `p2p`？

**`p2p-avail`** = 打洞握手成功，但 PONG 或真实数据帧还没到。

**正常 1-2 秒内**会升级到 `p2p`：

```
[P2P] 从 pc-a 收到 Punch → P2PAvailable（等待数据帧验证）
[P2P] 收到 PONG → P2P 直连（双向通道已验证）
```

**一直停在 `p2p-avail`** → 30 秒后自动降级到 TURN/WS。

---

## 🔧 从源码构建

### 前置要求

- Go 1.21+

### 构建

```bash
git clone https://github.com/goyo123321/n2n-termux.git
cd n2n-termux
go mod tidy

# 当前平台
go build -ldflags="-s -w" -o n2n-client .

# Termux (arm64)
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
  go build -ldflags="-s -w" -o n2n-client-linux-arm64 .

# Termux (armv7)
GOOS=linux GOARCH=arm CGO_ENABLED=0 GOARM=7 \
  go build -ldflags="-s -w" -o n2n-client-linux-armv7 .
```

**无外部依赖** —— 纯 Go 标准库 + `pion/stun` + `pion/turn` + `gorilla/websocket`。

---

## 📁 项目结构

```
n2n-termux/
├── main.go                       # 主程序（UDP 保活 + P2P 通道保活 + CA 自动检测）
├── packet_forwarder.go           # TCP 中继 + UDP 转发 + ICMP 响应（PC/Android → Termux）
├── ws_transport.go               # WebSocket 传输层（断线重连 + 90s 读超时）
├── relay_fallback.go             # 四级降级管理器
├── turn_client.go                # TURN 客户端
├── turn_lite.go                  # TURN-Lite 实现
├── turn_lite_tcp.go              # TURN over TCP / RFC 6062
├── nat_probe.go                  # NAT 类型探测
├── nathole_executor.go           # 打洞指令执行
├── p2p_stream.go                 # N2NS 可靠流协议
├── p2p_support.go                # P2P stream 依赖方法
├── punch.go                      # Punch/ACK 定义
├── socks5.go                     # SOCKS5 入口
├── app_stream.go                 # WS 应用层隧道（app_stream）
└── go.mod
```

---

## ⚠️ 限制

### Termux 特有

| 项 | 说明 |
|:---|:---|
| **无 TUN** | Android 内核拒绝非特权 App 创建 TUN |
| **主动仅 SOCKS5** | 应用**必须**显式配置代理，不支持系统级路由 |
| **主动仅 TCP** | SOCKS5 只代理 TCP，不支持 UDP / ICMP |
| **被动全支持** | PC/Android 可主动访问 Termux 的 TCP / UDP / ICMP |
| **ping 主动不通** | ICMP 不走 SOCKS5；但 PC/Android ping Termux 可以 |

### 权限

| 项 | 要求 |
|:---|:---|
| root | ❌ 不需要 |
| 特殊权限 | ❌ 不需要 |
| Termux 环境 | ✅ 需要 |
| CA 证书 | ⚠️ 首次需 `pkg install ca-certificates` |

### 与其他客户端共存

| 组合 | 可以吗 | 说明 |
|:---|:---|:---|
| n2n-termux 单独跑 | ✅ | 标准用法 |
| n2n-termux + n2n-android | ✅ | SOCKS5 与 VpnService 不冲突 |
| n2n-termux × 2 | ⚠️ | 两个 SOCKS5 会抢 `127.0.0.1:1080` 端口，需改 `SOCKS5_LISTEN` |
| n2n-termux + Clash | ⚠️ | 如果 Clash 也用 1080 端口会冲突 |

---

## 📊 性能参考

| 连接方式 | 延迟 | 消耗 |
|:---|:---|:---|
| **LAN 直连** | **<10ms** | **无** |
| P2P 直连（跨 NAT） | 20~60ms | 无 |
| **CGNAT hairpin** | **20~40ms** | **无** |
| TURN 中继 | 40~120ms | Cloudflare TURN 配额 |
| WebSocket 中继 | 100~300ms | Worker 请求配额 |

**注意**：Termux 端有 SOCKS5 代理层开销（约 5~10ms）。

---

## 🔗 与 edge-signal 配合

| 项 | edge-signal | n2n-termux |
|:---|:---|:---|
| 部署位置 | Cloudflare 边缘 | Android Termux |
| 角色 | 信令服务器 + TURN 凭证下发 + 中继兜底 | SOCKS5 + LAN/P2P/TURN 连接 + packet-forwarder |
| 语言 | TypeScript | Go |
| 存储 | Durable Objects | 本地 |

**先部署 edge-signal，再运行 n2n-termux。**

---

## 📄 License

MIT

## 🔗 相关项目

| 项目 | 平台 | 模式 | 互通 |
|:---|:---|:---|:---|
| [edge-signal](https://github.com/goyo123321/edge-signal) | Cloudflare Workers | 信令服务器 | — |
| [n2n-go-client](https://github.com/goyo123321/n2n-go-client) | Linux / macOS / Windows | TUN | ✅ 全部 |
| **n2n-termux** | Android Termux | **SOCKS5** | ✅ 全部 |
| [n2n-android](https://github.com/goyo123321/n2n-android) | Android APK | VpnService | ✅ 全部 |

**三端互通**：n2n-termux ↔ n2n-go-client ↔ n2n-android 可以互相访问（主动方向与协议支持见上表）。
