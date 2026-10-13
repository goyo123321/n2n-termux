# n2n-termux

**n2n 组网系统的 Termux 客户端**——基于 SOCKS5 代理，无需 root。

配合 [edge-signal](https://github.com/goyo123321/edge-signal) 使用，通过 Cloudflare Workers 信令服务器建立 **LAN 直连 / P2P / TURN / WebSocket 中继** 四级降级连接。

**与 [n2n-go-client](https://github.com/goyo123321/n2n-go-client)、[n2n-android](https://github.com/goyo123321/n2n-android) 完全互通**——Termux 通过 SOCKS5 访问其他节点的服务，反之亦然。

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

### 三种客户端定位对比

| 维度 | n2n-go-client | **n2n-termux**（本仓库） | n2n-android |
|:---|:---|:---|:---|
| **运行环境** | Linux / macOS / Windows / 路由器 | Android Termux | Android APK |
| **数据面** | TUN 虚拟网卡 | SOCKS5 代理 | VpnService（TUN） |
| **权限要求** | root / 管理员 | **无 root** | 无 root |
| **应用透明** | ✅ 系统级 | ⚠️ 需 App 显式配 SOCKS5 | ✅ 系统级 |
| **协议支持** | TCP / UDP / ICMP | **仅 TCP** | TCP / UDP / ICMP |
| **使用方式** | `curl http://10.64.0.X` | `curl --socks5 127.0.0.1:1080 ...` | 同 TUN |
| **适用场景** | 服务器 / 桌面 / 路由器 | **临时测试 / 无 root 设备** | 日常使用（手机） |

---

## 💡 为什么选择 n2n-termux

### 不需要 root

**Android 内核拒绝非特权 App 创建 TUN 网卡**。所以想在无 root 的 Android 设备上跑 n2n，只能走 SOCKS5 代理模式——这就是 n2n-termux 的定位。

### 与其他客户端的协作

| 场景 | 用哪个 | 理由 |
|:---|:---|:---|
| 日常手机使用 | n2n-android | VpnService，应用透明 |
| 临时调试 / 跑脚本 | **n2n-termux** | 无需 root，快速启动 |
| 与其他 n2n 客户端共存 | **n2n-termux** | SOCKS5 不抢 VPN 权限 |
| 已 root 且要完整数据面 | n2n-go-client（Termux+root） | 完整 TUN 数据面 |
| 路由器做网关 | n2n-go-client | 系统级路由 |

### 与 n2n-android 共存

**可以同时运行**：

- n2n-android 用 `VpnService`（走 TUN）
- n2n-termux 用 `SOCKS5`（走 `127.0.0.1`）
- 两者数据面路径独立，互不冲突

**但同一类型只能开一个**：两个 `VpnService` 会抢权限。

---

## ✨ 特性

- **无需 root** — 纯 SOCKS5 模式，任何 Android 设备都能跑
- **四级降级** — `LAN → P2P → TURN → WS`，连接永远可用
- **P2P 直连** — STUN 探测 + NAT 打洞，延迟低至 10ms
- **多目标打洞** — LAN + STUN 反射 + 观测 raddr 同时扫描，命中率大幅提升
- **CGNAT hairpin** — 同 STUN 出口 IP 时尝试端口扫描 + probe 回发
- **UDP 保活** — 每 5 秒刷新 STUN 映射，防止 CGNAT 端口漂移
- **P2P 通道保活** — 每 15 秒往对端发 N2NS PING 多目标探针
- **TURN 中继** — 支持 Cloudflare TURN + 自建 coturn（UDP / TCP / RFC6062）
- **中继回退** — TURN 失败自动降级到 WebSocket 中继
- **断线重连** — WebSocket 自动重连 + 90 秒读超时检测下行僵死
- **一键部署** — `install.sh` 全自动安装
- **CLIENT_ID 持久化** — 重启后虚拟 IP 不变
- **虚拟网段可配** — 服务端通过 `VIRTUAL_NETWORK` 下发
- **与 PC / Android APK 无缝互通**

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
│                       ↓                   │
│                  ┌──────────┐            │
│                  │ UDP/WS   │            │
│                  │ 传输层    │            │
│                  └──────────┘            │
└──────────────────────────────────────────┘
```

**与 PC 端的区别**：

- PC：TUN 内核栈 → 应用无感知
- Termux：SOCKS5 代理 → 应用**必须显式配置**走代理

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

### 为什么需要保活

**Android 上 CGNAT 更激进**——手机息屏后运营商 NAT 通常 TTL 更短。UDP 映射被回收后，P2P 通道立即断裂。

### 两层保活

| 层 | 间隔 | 目标 | 作用 |
|:---|:---|:---|:---|
| **UDP 保活** | 5 秒 | 3 个 STUN 服务器 | 保 socket 的 CGNAT 映射 |
| **P2P 通道保活** | 15 秒 | 每个 P2P 对端 | 保 A→B 会话 |

**流量代价**：
- UDP 保活：~50 MB/天
- P2P 保活：~180 KB/天 × 对端数

**真实数据帧天然刷新 CGNAT 映射**。保活只在**空闲时**兜底。

---

## 🚀 快速开始

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

### 使用 SOCKS5

**必须显式走 SOCKS5**——因为 n2n-termux 只提供 `127.0.0.1:1080` 代理入口。

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

### 场景 D：与 Android APK 混合

同一台 Android 设备**可以同时**跑：

- **n2n-android APK**（VpnService）——系统级应用透明
- **n2n-termux**（SOCKS5）——脚本临时调试

**互不冲突**：VpnService 走 TUN，Termux 走 SOCKS5。

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
| `CONNECT_TOKEN` | ⚠️ | `""` | 连接密码（Worker 配了才需要）|
| `ROOM_ID` | ❌ | `default-room` | 房间名。只允许 `[A-Za-z0-9_-]+` |
| `CLIENT_ID` | ❌ | 自动生成并持久化 | 客户端唯一 ID |
| `NODE_NAME` | ❌ | hostname | 显示名 |
| `SOCKS5_LISTEN` | ❌ | `127.0.0.1:1080` | SOCKS5 监听地址 |

**虚拟网段**由服务端 `wrangler.toml` 的 `VIRTUAL_NETWORK` 决定，客户端从 `ready` 消息自动接收。

**修改配置后**需要重启客户端。

---

## 📋 启动日志

启动成功后：

```
n2n-termux dev 启动
[配置] 未设置 CONNECT_TOKEN
[配置] 生成并持久化 CLIENT_ID: termux-a-1704067200 -> /data/data/com.termux/files/home/.n2n-termux/client_id
[P2P] UDP 监听端口 42529
[NAT] 启动预收集 LAN: [...] 或 [NAT] 启动预收集 LAN 为空（可能蜂窝网络）
[NAT] STUN 74.125.250.129:19302 → 120.239.134.13:19190
[NAT] HardNAT（对称），ports_diff=0 pub=120.239.134.13:19190
[WS] 连接 wss://edge-signal.xxx.workers.dev/ws/myroom?cid=termux-a
已连接信令，Client ID: termux-a-1704067200，节点名: Android Termux
分配虚拟 IP: 10.64.0.3（网段 10.64.0.0/24）
[信令] 服务端看到的本机出口 IP: 120.229.199.61
[NAT] 上报 LAN 地址: [...]
[信令] 上报 p2p_metadata: natType=HardNAT publicEndpoint="120.239.134.13:19190" multiExit=true assisted=2 lan=1
[TURN] 获取到 custom TURN 服务器: turn:xxx:3478
[TURN] 中继地址: 5.6.7.8:50000
[Keepalive] 启动，每 5s 刷新 3 个 STUN 服务器
[P2P-Keepalive] 启动，每 15s 刷新 P2P 通道
[SOCKS5] 监听 127.0.0.1:1080（用 curl --socks5 或 ALL_PROXY 访问虚拟 IP）

================= 本机信息 =================
  Client ID   : termux-a-1704067200
  节点名       : Android Termux
  虚拟 IP     : 10.64.0.3
  虚拟网段    : 10.64.0.0/24
  P2P 端口    : 42529
==========================================
```

**对端上线**：

```
========== 节点就绪: pc-a ==========
  虚拟 IP   : 10.64.0.2
  公网地址  : 1.2.3.4:54321
  LAN 地址  : [192.168.1.100:54321]
  STUN 端点 : [1.2.3.4:54321 1.2.3.4:54335]
==========================================

[P2P] 从 pc-a (192.168.1.100) 收到 Punch → P2PAvailable（等待数据帧验证）
[P2P] 收到 PONG → P2P 直连（双向通道已验证）
[连接] pc-a → P2P 直连
```

---

## 🔍 与 PC / Android 的数据面差异

### 数据面协议对比

| 端 | 数据面 | 协议支持 | 应用透明 |
|:---|:---|:---|:---|
| **n2n-go-client (PC)** | TUN | TCP / UDP / ICMP | ✅ |
| **n2n-android (APK)** | VpnService | TCP / UDP / ICMP | ✅ |
| **n2n-termux (本仓库)** | SOCKS5 | **仅 TCP** | ⚠️ |

### 互通机制

**三端在服务端和 P2P 通道层用同一套协议**：

- N2NS 帧（21 字节头）
- Punch/ACK 握手
- 打洞指令（`nat_hole_instruction`）
- 状态机（`P2PAvailable` → `P2P`）

**所以完全互通**：

| 主动方 | 被动方 | 数据面路径 |
|:---|:---|:---|
| PC（TUN） | Termux（SOCKS5） | PC 发 N2NS → Termux 收到后转发到本地 TCP |
| Termux（SOCKS5） | PC（TUN） | Termux 发 N2NS → PC 收到后写入 TUN |
| Termux（SOCKS5） | Android（VpnService） | Termux 发 N2NS → Android 收到后写入 TUN |
| Android（VpnService） | Termux（SOCKS5） | Android 发 N2NS → Termux 侧转发到本地 TCP |

**唯一区别**：Termux 端没有 `peer.UDPAddr` 直发路径（不会直接往虚拟 IP 发包），只能通过 SOCKS5 触发。

### 打洞行为一致

**Termux ↔ PC** 与 **Termux ↔ Termux** 打洞行为一致：

- 双端都上报 `lanEndpoints` → LAN 直连优先
- 双端都上报 `assistedEndpoints` → STUN 反射多端点
- 打洞指令由同一个 `coordinator.js` 生成
- 状态机完全相同

---

## 🔧 从源码构建

### 前置要求

- Go 1.21+
- Android NDK（可选，交叉编译用）

### 构建

```bash
git clone https://github.com/goyo123321/n2n-termux.git
cd n2n-termux
go mod tidy

# 当前平台
go build -o n2n-client .

# Termux (arm64)
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o n2n-client-linux-arm64 .

# Termux (armv7)
GOOS=linux GOARCH=arm CGO_ENABLED=0 GOARM=7 go build -o n2n-client-linux-armv7 .
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
├── main.go                       # 主程序（含 UDP 保活 + P2P 通道保活）
├── ws_transport.go               # WebSocket 传输层（断线重连 + 90s 读超时）
├── relay_fallback.go             # 四级降级管理器
├── turn_client.go                # TURN 客户端
├── turn_lite.go                  # TURN-Lite 实现
├── turn_lite_tcp.go              # TURN over TCP
├── nat_probe.go                  # NAT 类型探测
├── nathole_executor.go           # 打洞指令执行
├── p2p_stream.go                 # N2NS 可靠流协议
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
| **仅 SOCKS5** | 应用**必须**显式配置代理，不支持系统级路由 |
| **仅 TCP** | SOCKS5 只代理 TCP，不支持 UDP / ICMP |
| **ping 不通** | ICMP 不走 SOCKS5 |

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

**注意**：Termux 端有 SOCKS5 代理层开销（约 5~10ms）。PC 端直接走 TUN，无此开销。

---

## 🐛 常见问题

### Q: 无 root 真的能用吗？

**能**。Termux 本身不需要 root，SOCKS5 模式也不需要特殊权限。

**但要清楚限制**：
- 应用**必须**显式走 SOCKS5 代理（不能用 `ping`，不能直接 `curl http://10.64.0.x`）
- 仅支持 TCP（不支持 UDP / ICMP）
- 端口固定 `127.0.0.1:1080`（可改 `SOCKS5_LISTEN`）

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

**没走 SOCKS5**。默认 `curl` 直连，会尝试通过系统网络访问 `10.64.0.3`——但该地址在系统里不存在。

**必须显式走 SOCKS5**：

```bash
# 方式 1：命令行参数
curl --socks5 127.0.0.1:1080 http://10.64.0.3:8080/

# 方式 2：环境变量（会话级）
export ALL_PROXY=socks5://127.0.0.1:1080
curl http://10.64.0.3:8080/

# 方式 3：永久写入 shell 配置
echo 'export ALL_PROXY=socks5://127.0.0.1:1080' >> ~/.bashrc
```

### Q: PC 访问 Termux 服务失败？

**Termux 端服务必须监听 `0.0.0.0`**。

```bash
# ✅ 可以（PC 能访问）
python3 -m http.server 8080 --bind 0.0.0.0

# ❌ 不行（只监听 loopback，PC 访问不到）
python3 -m http.server 8080 --bind 127.0.0.1
```

因为 n2n-termux 是 SOCKS5 代理，PC 通过虚拟 IP 访问 Termux 时，N2NS 帧到达 Termux 后会被转发到 `127.0.0.1:port`。但如果服务只监听 `127.0.0.1`，**PC 侧无法触发 N2NS 帧**。

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

### Q: 手动指定 SOCKS5 端口

编辑 `~/.n2n-termux/config.env`：

```bash
SOCKS5_LISTEN="127.0.0.1:1081"
```

然后重启客户端。

### Q: 怎么看日志？

```bash
# 前台运行时，直接看
~/.n2n-termux/start.sh

# 后台运行时
tail -f ~/n2n.log

# 或过滤关键字
tail -f ~/n2n.log | grep -E "\[P2P\]|\[NAT-HOLE\]|\[连接\]"
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

## 🔗 与 edge-signal 配合

| 项 | edge-signal | n2n-termux |
|:---|:---|:---|
| 部署位置 | Cloudflare 边缘 | Android Termux |
| 角色 | 信令服务器 + TURN 凭证下发 + 中继兜底 | 建立 SOCKS5 + LAN/P2P/TURN 连接 |
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

**三端互通**：n2n-termux ↔ n2n-go-client ↔ n2n-android 可以互相访问。
