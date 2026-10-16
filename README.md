# n2n-termux

**n2n 组网系统的 Termux 客户端**——SOCKS5 + 双向 IP 转发，无需 root。

配合 [edge-signal](https://github.com/goyo123321/edge-signal) 使用，通过 Cloudflare Workers 信令服务器建立 **LAN 直连 / P2P / TURN / WebSocket 中继** 四级降级连接。

**与 [n2n-go-client](https://github.com/goyo123321/n2n-go-client)、[n2n-android](https://github.com/goyo123321/n2n-android) 完全互通**：

- **Termux → 其他端**：通过 SOCKS5 代理（`127.0.0.1:1080`）
- **其他端 → Termux**：通过内置 `packet_forwarder` 转发到本地服务（TCP / UDP / ICMP 全支持）

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
   │ TUN 模式  │       │SOCKS5 +  │        │VpnService│
   │ 系统级路由│       │包转发器  │        │ 无 root  │
   │ 需 root  │       │ 无 root  │        │          │
   └──────────┘       └──────────┘        └──────────┘
     Linux/macOS         Android              Android
     Windows             Termux                APK
     路由器

       LAN / P2P / TURN / WS  --  完全互通
```

### 三种客户端定位对比

| 维度 | n2n-go-client | **n2n-termux**（本仓库） | n2n-android |
|:---|:---|:---|:---|
| **运行环境** | Linux / macOS / Windows / 路由器 | Android Termux | Android APK |
| **数据面** | TUN 虚拟网卡 | SOCKS5 + 包转发器 | VpnService（TUN） |
| **权限要求** | root / 管理员 | **无 root** | 无 root |
| **应用透明** | ✅ 系统级 | ⚠️ 需 App 显式配 SOCKS5 | ✅ 系统级 |
| **主动访问其他端** | ✅ TCP/UDP/ICMP | ⚠️ **仅 TCP**（SOCKS5 限制） | ✅ TCP/UDP/ICMP |
| **被其他端访问** | ✅ TCP/UDP/ICMP | ✅ **TCP/UDP/ICMP** | ✅ TCP/UDP/ICMP |
| **使用方式** | `curl http://10.64.0.X` | `curl --socks5 127.0.0.1:1080 ...` | 同 TUN |
| **适用场景** | 服务器 / 桌面 / 路由器 | **无 root 设备 / 临时调试** | 日常使用（手机） |

---

## 💡 为什么选择 n2n-termux

### 不需要 root

**Android 内核拒绝非特权 App 创建 TUN 网卡**。想在无 root 的 Android 设备上跑 n2n，只能走 **SOCKS5 + 用户态包转发**。

### 双向互通（关键特性）

| 方向 | 路径 | 支持协议 |
|:---|:---|:---|
| **Termux 主动访问其他端** | SOCKS5 代理 | **仅 TCP** |
| **其他端访问 Termux** | 内置 packet_forwarder | **TCP / UDP / ICMP** |

所以 **PC/Android 可以完整访问 Termux 上的所有服务**——HTTP、SSH、DNS、游戏服务器，**甚至能 ping 通**。

### 与其他客户端的协作

| 场景 | 用哪个 |
|:---|:---|
| 日常手机使用 | n2n-android（VpnService，应用透明） |
| 临时调试 / 跑脚本 | **n2n-termux**（无需 root，快速启动） |
| 与其他 n2n 客户端共存 | **n2n-termux**（SOCKS5 不抢 VPN 权限） |
| 已 root 且要完整数据面 | n2n-go-client（Termux+root） |
| 路由器做网关 | n2n-go-client |

---

## ✨ 特性

- **无需 root** — 纯 SOCKS5 + 用户态转发，任何 Android 设备都能跑
- **双向互通** — 内置 `packet_forwarder`，其他端可访问 Termux 上的 TCP/UDP/ICMP 服务
- **四级降级** — `LAN → P2P → TURN → WS`，连接永远可用
- **P2P 直连** — STUN 探测 + NAT 打洞，延迟低至 10ms
- **多目标打洞** — LAN + STUN 反射 + 观测 raddr 同时扫描，命中率大幅提升
- **CGNAT hairpin** — 同 STUN 出口 IP 时尝试端口扫描 + probe 回发
- **UDP 保活** — 每 5 秒刷新 STUN 映射，防止 CGNAT 端口漂移
- **P2P 通道保活** — 每 15 秒往对端发 N2NS PING 多目标探针
- **TURN 中继** — 支持 Cloudflare TURN + 自建 coturn
- **中继回退** — TURN 失败自动降级到 WebSocket 中继
- **断线重连** — WebSocket 自动重连 + 90 秒读超时检测下行僵死
- **一键部署** — `install.sh` 全自动安装
- **CLIENT_ID 持久化** — 重启后虚拟 IP 不变
- **CA 证书自动检测** — 无需手动 `export SSL_CERT_FILE`
- **虚拟网段可配** — 服务端通过 `VIRTUAL_NETWORK` 下发

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
│                       ↑                   │
│                  ┌──────────┐            │
│   ┌──────────┐   │ 包转发器  │            │
│   │ 其他端 →  │──→│ TCP/UDP/ │            │
│   │ 虚拟 IP   │   │ ICMP     │            │
│   └──────────┘   └────┬─────┘            │
│                       │                   │
│                       ↓                   │
│                  ┌──────────┐            │
│                  │ UDP/WS   │            │
│                  │ 传输层    │            │
│                  └──────────┘            │
└──────────────────────────────────────────┘
```

**双向数据流**：

- **出方向**：App 请求 → SOCKS5 → N2NS 帧 → P2P/TURN/WS → 对端
- **入方向**：对端 → 裸 IPv4 包 → packet_forwarder 解析 → 127.0.0.1:port

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

## 🚀 快速开始

### 前置要求

- Android + Termux（从 [F-Droid](https://f-droid.org/packages/com.termux/) 或 [GitHub Releases](https://github.com/termux/termux-app/releases) 安装，**不要用 Google Play 版**，Play 版功能受限）
- 已部署的 [edge-signal](https://github.com/goyo123321/edge-signal) Worker

### 一键安装

在 Termux 里：

```bash
curl -fsSL https://raw.githubusercontent.com/goyo123321/n2n-termux/main/install.sh | bash
```

或非交互式：

```bash
SIGNALING_URL="wss://edge-signal.xxx.workers.dev" \
ROOM_ID="myroom" \
CLIENT_ID="termux-a" \
AUTO_START=1 \
  bash install.sh
```

**安装脚本会**：

1. 检测架构（arm64 / armv7 / amd64）
2. 下载对应架构的二进制
3. 生成配置文件 `~/.n2n-termux/config.env`
4. 生成启动脚本 `~/.n2n-termux/start.sh`
5. 可选自动启动

### 手动启动

```bash
~/.n2n-termux/start.sh
```

**后台启动**：

```bash
nohup ~/.n2n-termux/start.sh > ~/n2n.log 2>&1 &
```

**停止**：

```bash
pkill -f n2n-client
```

### 使用 SOCKS5（主动访问其他端）

**必须显式走 SOCKS5**——Termux 端只提供 `127.0.0.1:1080` 代理入口。

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

### 被其他端访问（内置包转发器）

**只需在 Termux 端启动服务**，PC/Android 就能通过虚拟 IP 访问：

```bash
# TCP 服务（HTTP 测试）
python3 -m http.server 8080 &

# UDP 服务（echo 测试）
pkg install socat
socat -v UDP-RECVFROM:5353,fork EXEC:cat &

# SSH 服务
pkg install openssh
sshd
# 默认监听 8022
```

**PC/Android 端**（TUN 模式）：

```bash
# ICMP
ping 10.64.0.3

# TCP
curl http://10.64.0.3:8080/

# UDP
echo "hello" | nc -u 10.64.0.3 5353

# SSH
ssh -p 8022 u0_aXXX@10.64.0.3
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

### 场景 B：PC/Android 访问 Termux 上的服务

**Termux 端起服务**：

```bash
# 监听 0.0.0.0 或 127.0.0.1 都可以
# packet_forwarder 会 dial 127.0.0.1:port
python3 -m http.server 8080 &
sshd  # 默认监听 8022
```

**PC 端直接访问**（走 TUN，无需配置）：

```bash
ping 10.64.0.3                    # ICMP
curl http://10.64.0.3:8080/       # TCP
ssh -p 8022 u0_aXXX@10.64.0.3     # SSH
```

### 场景 C：Termux ↔ Termux

**主动方**：

```bash
curl --socks5 127.0.0.1:1080 http://10.64.0.4:8080/
```

**被动方**：

```bash
python3 -m http.server 8080 &
```

### 场景 D：三端混合

同一台 Android 设备**可以同时**跑：

- **n2n-android APK**（VpnService）——系统级应用透明
- **n2n-termux**（SOCKS5）——脚本临时调试

**互不冲突**：VpnService 走 TUN，Termux 走 SOCKS5 + 包转发。

---

## ⚙️ 配置项

### `~/.n2n-termux/config.env`

```bash
SIGNALING_URL="wss://edge-signal.xxx.workers.dev"
CONNECT_TOKEN=""
ROOM_ID="default-room"
CLIENT_ID="termux-a"
NODE_NAME="Android Termux"
SOCKS5_LISTEN="127.0.0.1:1080"
```

| 变量 | 必填 | 默认值 | 说明 |
|:---|:---|:---|:---|
| `SIGNALING_URL` | ✅ | — | edge-signal 的 WSS 地址 |
| `CONNECT_TOKEN` | ⚠️ | `""` | 连接密码（Worker 配了才需要） |
| `ROOM_ID` | ❌ | `default-room` | 房间名。只允许 `[A-Za-z0-9_-]+` |
| `CLIENT_ID` | ❌ | 自动生成并持久化 | 客户端唯一 ID |
| `NODE_NAME` | ❌ | hostname | 显示名 |
| `SOCKS5_LISTEN` | ❌ | `127.0.0.1:1080` | SOCKS5 监听地址 |
| `STUN_SERVERS` | ❌ | Google + Cloudflare | 自定义 STUN 服务器（逗号分隔） |

**虚拟网段**由服务端 `wrangler.toml` 的 `VIRTUAL_NETWORK` 决定，客户端从 `ready` 消息自动接收。

**修改配置后**需要重启客户端。

---

## 📋 启动日志

启动成功后：

```
[TLS] CA 证书: /data/data/com.termux/files/usr/etc/tls/cert.pem (xxx bytes)
[DNS] 使用 /data/data/com.termux/files/usr/etc/resolv.conf 的 2 个 nameserver: [8.8.8.8:53 8.8.4.4:53]
n2n-termux dev 启动
[FWD] 已启动（TCP 中继 + UDP 转发 + ICMP 响应）
[P2P] UDP 监听端口 44145
[NAT] 启动预收集 LAN: [192.168.x.x:44145]
[NAT] STUN 74.125.250.129:19302 → 120.239.x.x:47385
[NAT] HardNAT（对称），ports_diff=3 pub=120.239.x.x:47385
[WS] 连接 wss://edge-signal.xxx.workers.dev/ws/default-room?cid=termux-b
已连接信令，Client ID: termux-b，节点名: Termux-B
分配虚拟 IP: 10.64.0.3（网段 10.64.0.0/24）
[信令] 服务端看到的本机出口 IP: 120.229.x.x
[NAT] 上报 LAN 地址: [...]
[信令] 上报 p2p_metadata: natType=HardNAT publicEndpoint="..." multiExit=true assisted=2 lan=1
[信令] ready: 返回 2 个已有节点
[信令] 已有节点: pc-a vip=10.64.0.2 ...

========== 节点就绪: pc-a ==========
  虚拟 IP   : 10.64.0.2
  公网地址  : 1.2.3.4:54321
  LAN 地址  : [192.168.1.100:54321]
==========================================

[TURN] 获取到 custom TURN: turn:...
[TURN] 中继地址: 5.6.7.8:50000
[Keepalive] 启动，每 5s 刷新 5 个 STUN 服务器
[P2P-Keepalive] 启动，每 15s 刷新 P2P 通道
[SOCKS5] 监听 127.0.0.1:1080

================= 本机信息 =================
  Client ID   : termux-b
  节点名       : Termux-B
  虚拟 IP     : 10.64.0.3
  虚拟网段    : 10.64.0.0/24
  P2P 端口    : 44145
  模式         : SOCKS5 + packet-forwarder（双向互通）
==========================================
```

**PC 访问 Termux 时**：

```
[FWD-ICMP] Echo Request from 10.64.0.2
[FWD-TCP] + 10.64.0.2:xxxxx → 127.0.0.1:8080 (iss=..., rcv=...)
[FWD-TCP] ✓ established 10.64.0.2:xxxxx
[FWD-UDP] + 10.64.0.2:xxxxx → 127.0.0.1:5353
```

**Termux 访问 PC 时**：

```
[SOCKS5] CONNECT 10.64.0.2:8080
[SOCKS5] CONNECT 10.64.0.2:8080 → P2P 直连 (stream=4)
```

---

## 🔧 从源码构建

### 前置要求

- Go 1.21+
- Termux 环境

### 安装依赖（Termux）

```bash
pkg update
pkg install golang git ca-certificates
```

### 编译

```bash
git clone https://github.com/goyo123321/n2n-termux.git
cd n2n-termux
go mod tidy

# 本地编译
go build -ldflags="-s -w" -o n2n-client .

# 交叉编译（从 PC 编译 ARM64 给 Termux 用）
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
  go build -ldflags="-s -w" -o n2n-client-linux-arm64 .
```

### GitHub Actions 自动构建

```
Actions → Build n2n-termux → Run workflow
  version: v1.0.0
  publish: ✓
```

---

## 📁 项目结构

```
n2n-termux/
├── main.go                       # 主程序（含 CA 自动检测 + UDP 保活 + P2P 通道保活）
├── packet_forwarder.go           # ★ TCP 中继 + UDP 转发 + ICMP 响应
├── ws_transport.go               # WebSocket 传输层（断线重连 + 90s 读超时）
├── relay_fallback.go             # 四级降级管理器
├── turn_client.go                # TURN 客户端
├── turn_lite.go                  # TURN-Lite 实现
├── turn_lite_tcp.go              # TURN over TCP
├── nat_probe.go                  # NAT 类型探测
├── nathole_executor.go           # 打洞指令执行
├── p2p_stream.go                 # N2NS 可靠流协议
├── p2p_support.go                # P2P stream 依赖方法
├── punch.go                      # Punch/ACK 定义
├── socks5.go                     # SOCKS5 入口
├── app_stream.go                 # WS 应用层隧道
├── install.sh                    # 一键部署脚本
└── go.mod
```

---

## ⚠️ 限制

### Termux 特有

| 项 | 说明 |
|:---|:---|
| **无 TUN** | Android 内核拒绝非特权 App 创建 TUN |
| **主动访问** | 仅 SOCKS5（**仅 TCP**） |
| **被动接受** | 完整支持（**TCP/UDP/ICMP**） |
| **SOCKS5 不支持 UDP/ICMP** | SOCKS5 协议本身限制 |

### 权限

| 项 | 要求 |
|:---|:---|
| root | ❌ 不需要 |
| 特殊权限 | ❌ 不需要 |
| Termux 环境 | ✅ 需要 |

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

**注意**：Termux 端有 SOCKS5 代理层 + 用户态转发开销（约 5~15ms）。PC 端直接走 TUN，无此开销。

---

## 🐛 常见问题

### Q: 无 root 真的能用吗？

**能**。Termux 本身不需要 root，SOCKS5 模式也不需要特殊权限。

**但要清楚限制**：

- **主动访问**（Termux → 其他）：仅 TCP，需显式走 SOCKS5 代理
- **被动接受**（其他 → Termux）：TCP / UDP / ICMP 全支持，无需配置

### Q: `tls: failed to verify certificate: x509: certificate signed by unknown authority`？

**Termux 缺少 CA 证书**，Go 无法验证 TLS。

**修复**：

```bash
pkg install ca-certificates
```

**如果还报错**，检查证书路径：

```bash
ls -la $PREFIX/etc/tls/cert.pem
```

**如果文件不存在**，从系统路径复制：

```bash
mkdir -p $PREFIX/etc/tls
cat /system/etc/security/cacerts/* > $PREFIX/etc/tls/cert.pem
```

**最新版客户端内置 CA 自动检测**，会打印：

```
[TLS] CA 证书: /data/data/com.termux/files/usr/etc/tls/cert.pem (xxx bytes)
```

如果没这行，说明二进制太旧，需要升级。

### Q: 为什么 `ping 10.64.0.3` 从 PC 端不通？

**分两种情况**：

**情况 1：单向 P2P（最常见）**

PC → Termux 通，Termux → PC 不通。日志里会看到 **每秒一次** 的 `[FWD-ICMP] Echo Request from 10.64.0.2`（PC 一直重发因为没收到 reply）。

**原因**：CGNAT 打洞单向——PC 的包能到 Termux，但 Termux 的包被 PC 侧 CGNAT 丢弃。

**修复**：最新版代码里 **ICMP 强制走 WS 中继**（牺牲延迟换可靠）：

```go
// packet_forwarder.go sendToPeer
if len(pkt) > 9 && pkt[9] == protoICMP {
    if f.edge.ws != nil {
        _ = f.edge.ws.SendBinary(pkt)
    }
    return
}
```

RTT 会变成 100-300ms，但 **一定能通**。

**情况 2：Termux 服务未启动**

`packet_forwarder` 只响应 ICMP，不需要 Termux 端启动任何服务。

如果 PC ping 完全没反应，检查：

1. Termux 客户端是否在运行（`ps aux | grep n2n-client`）
2. Termux 日志里是否有 `[FWD] 已启动`
3. PC 端 `ip route` 是否有 `10.64.0.0/24` 路由

### Q: `curl http://10.64.0.3:8080/` 从 PC 端不通？

**排查步骤**：

1. **Termux 端服务是否启动**？

   ```bash
   # Termux 里
   python3 -m http.server 8080 &
   netstat -tlnp | grep 8080
   ```

   必须监听 `127.0.0.1:8080` 或 `0.0.0.0:8080`。

2. **Termux 端日志有没有 `[FWD-TCP] + 10.64.0.2:xxxxx → 127.0.0.1:8080`**？

   - **有** → packet_forwarder 收到请求，问题在响应回程。**TCP 也可能单向 P2P**，同样强制走 WS 中继：

     ```go
     // 在 packet_forwarder.go 的 sendToPeer 里
     // 把 TCP 也加入强制走 WS 中继
     if len(pkt) > 9 && (pkt[9] == protoICMP || pkt[9] == protoTCP) {
         if f.edge.ws != nil {
             _ = f.edge.ws.SendBinary(pkt)
         }
         return
     }
     ```

   - **没有** → 请求没到 Termux。检查 P2P 状态：

     ```bash
     # 服务端面板
     https://你的worker.workers.dev/admin?token=<ADMIN_TOKEN>
     ```

     看两端的"连接状态"，应该是 `p2p-BD` 或 `ws-BD`。

3. **PC 端 `curl -v`**：

   ```bash
   curl -v --max-time 10 http://10.64.0.3:8080/
   ```

   - `Connection timed out` → 包丢了
   - `Connection refused` → 到了 Termux 但没有服务
   - `Empty reply from server` → 建立连接但无响应

### Q: Termux 主动 ping 其他端为什么不通？

**SOCKS5 不支持 ICMP**。这是协议限制，不是权限问题。

**要用 ICMP 请换用**：

- n2n-go-client（TUN 模式，root）
- n2n-android（VpnService）

### Q: 为什么 `curl http://10.64.0.3:8080/` 直接调用不通？

**没走 SOCKS5**。默认 `curl` 直连，会尝试通过系统网络访问——但该地址在系统里不存在。

**必须显式走 SOCKS5**：

```bash
curl --socks5 127.0.0.1:1080 http://10.64.0.3:8080/
```

### Q: 与其他 VPN 冲突？

**SOCKS5 模式不冲突**——SOCKS5 走 `127.0.0.1`，不抢 VPN 权限。

即使设备上已运行 Clash / n2n-android，n2n-termux 也能正常启动。

**唯一冲突点**：如果 Clash 也监听 `1080` 端口，会抢端口。改 `SOCKS5_LISTEN="127.0.0.1:1081"` 即可。

### Q: 打洞一直失败？

看日志 `[NAT-HOLE] ❌ 失败`。会自动降级到 TURN 中继。TURN 也不可用则降级到 WS 中继。

**排查**：

1. **`publicEndpoint` 为空** → NAT 探测失败，STUN 服务器被封
2. **端口差 > 100** → CGNAT 映射漂移严重
3. **同 STUN 出口 IP** → 同 CGNAT，需要 hairpin

### Q: `[FWD-ICMP] Echo Request from 10.64.0.2` 每秒一次刷屏？

**单向 P2P 问题**（见上文）。Termux 收到 PC 的 ping request，但 reply 没回到 PC，PC 一直重发。

**修复**：更新到最新版代码，ICMP 强制走 WS 中继。

### Q: 手动指定 SOCKS5 端口

编辑 `~/.n2n-termux/config.env`：

```bash
SOCKS5_LISTEN="127.0.0.1:1081"
```

重启客户端。

### Q: 怎么看日志？

```bash
# 前台运行时，直接看
~/.n2n-termux/start.sh

# 后台运行时
tail -f ~/n2n.log

# 或过滤关键字
tail -f ~/n2n.log | grep -E "\[P2P\]|\[NAT-HOLE\]|\[FWD|\[连接\]"
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

---

## 🔗 与 edge-signal 配合

| 项 | edge-signal | n2n-termux |
|:---|:---|:---|
| 部署位置 | Cloudflare 边缘 | Android Termux |
| 角色 | 信令服务器 + TURN 凭证下发 + 中继兜底 | SOCKS5 + 包转发 + LAN/P2P/TURN 连接 |
| 语言 | TypeScript | Go |
| 存储 | Durable Objects | 本地 |

**先部署 edge-signal，再运行 n2n-termux。**

---

## 🔍 数据面原理

### SOCKS5 出口（Termux → 其他）

```
App → SOCKS5 CONNECT → 触发 N2NS SYN → 对端 acceptSYN
                                      ↓
                                  对端 dial 127.0.0.1:port
                                      ↓
                    N2NS DATA 帧双向传输
```

### packet_forwarder 入口（其他 → Termux）

```
对端发裸 IPv4 包 → Termux udpReadLoop / WS / TURN 收到
                    ↓
            pktFwd.HandlePacket(pkt)
                    ↓
        ┌───────────┼───────────┐
        ↓           ↓           ↓
    ICMP         TCP         UDP
        ↓           ↓           ↓
  构造 Echo Reply  dial 127    dial 127
                  .0.0.1:port  .0.0.1:port
        ↓           ↓           ↓
        └───────────┼───────────┘
                    ↓
           sendToPeer（ICMP 走 WS，TCP/UDP 走 P2P）
```

### 为什么 ICMP 强制走 WS？

**单向 P2P 是常见问题**：CGNAT 打洞后，A→B 的包能通，但 B→A 的包被丢弃。ICMP 依赖双向（Request + Reply），走 P2P 时 reply 丢失。

**WS 中继保证双向**，虽然慢但可靠。对 ICMP 这种低频、延迟不敏感的流量是最务实的选择。

---

## 📄 License

MIT

## 🔗 相关项目

| 项目 | 平台 | 模式 | 互通 |
|:---|:---|:---|:---|
| [edge-signal](https://github.com/goyo123321/edge-signal) | Cloudflare Workers | 信令服务器 | — |
| [n2n-go-client](https://github.com/goyo123321/n2n-go-client) | Linux / macOS / Windows | TUN | ✅ 全部 |
| **n2n-termux** | Android Termux | SOCKS5 + 包转发 | ✅ 全部 |
| [n2n-android](https://github.com/goyo123321/n2n-android) | Android APK | VpnService | ✅ 全部 |

**三端互通**：n2n-termux ↔ n2n-go-client ↔ n2n-android 可以互相访问。
