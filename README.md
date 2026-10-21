# n2n-termux

**n2n 组网系统的 Termux 客户端**——基于 SOCKS5 与用户态 Packet Forwarder，无需 root 即可实现双向互通。

配合 [edge-signal](https://github.com/goyo123321/edge-signal) 使用，通过 Cloudflare Workers 信令服务器建立 **LAN 直连 / P2P / TURN / WebSocket 中继** 四级降级连接。

**与 [n2n-go-client](https://github.com/goyo123321/n2n-go-client)、[n2n-android](https://github.com/goyo123321/n2n-android) 完全互通**。

---

## 🌐 四端协同全景

```text
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
   │ TUN 模式  │       │ SOCKS5   │        │VpnService│
   │ 系统级路由│       │ + Packet │        │ + SOCKS5 │
   │ 需 root  │       │ Forwarder│        │ 无 root  │
   └──────────┘       └──────────┘        └──────────┘
     Linux/macOS         Android              Android
     Windows             Termux                APK
     路由器

       LAN / P2P / TURN / WS  --  完全互通
```

---

## ✨ 核心特性

- **无需 root** — 纯 SOCKS5 模式，任何 Android 设备都能跑
- **双向互通** — 内置 `Packet Forwarder`，支持被其他设备主动访问（TCP/UDP/ICMP）
- **LAN 优先** — 同 WiFi 场景下优先走 LAN，RTT 稳定 5-15ms
- **自动 TLS 证书** — 内置 Termux CA 证书探测，彻底解决 `x509: unknown authority` 报错
- **自动 URL 补全** — `install.sh` 检测漏写 `wss://` 会自动补全
- **四级降级** — `LAN → P2P → TURN → WS`，连接永远可用
- **N2NW / N2NA 二进制** — WS 中继数据面用二进制帧，比 base64 省 ~27% 带宽
- **协议能力协商** — 上报 `features=["n2na","n2nw"]`，服务端自动协商帧格式，**混合房间零丢包**
- **单向 P2P 自动降级** — 与 PC/Android 一致，只收到 PING 而收不到 PONG 时 60 秒后自动降级到 TURN/WS
- **轻量无依赖** — 纯 Go 标准库，无需 gVisor，编译仅需 2 秒，二进制 ~10MB
- **UDP 保活** — 每 5 秒刷新 STUN 映射，防止 CGNAT 端口漂移
- **P2P 通道保活** — 每 15 秒往对端发 N2NS PING 多目标探针
- **零配置网段** — 服务端通过 `VIRTUAL_NETWORK` 下发，客户端自动适配
- **一键部署** — `install.sh` 自动下载 + 配置 + 启动

---

## 📂 安装路径（默认当前目录）

**从 v1.1.21 起，安装目录默认是「当前工作目录下的 `.n2n-termux`」**，不再是 `~/.n2n-termux`。

| 用户在哪个目录运行 `install.sh` | 实际安装到 |
|:---|:---|
| `~` | `~/.n2n-termux/` |
| `/sdcard/n2n` | `/sdcard/n2n/.n2n-termux/` |
| `~/my-project` | `~/my-project/.n2n-termux/` |
| 任意目录 `P` | `P/.n2n-termux/` |

**可以用 `INSTALL_DIR` 环境变量覆盖**：

```bash
INSTALL_DIR=/sdcard/n2n bash install.sh          # 直接装到 /sdcard/n2n
INSTALL_DIR="$(pwd)" bash install.sh             # 不建子目录，直接装当前目录
```

**安装完成后的目录**：

```
<当前工作目录>/.n2n-termux/
├── n2n-termux          # 二进制
├── config.env          # 配置
├── start.sh            # 前台启动
├── background.sh       # 后台启动（nohup）
├── stop.sh             # 停止
├── .version            # 当前版本号
├── client_id           # 持久化的 Client ID
├── n2n.pid             # 后台运行时的 PID
└── n2n.log             # 日志（>5MB 自动备份）
```

**`start.sh` / `background.sh` / `stop.sh` 三个脚本都能从任意目录调用**（用自身所在目录推导 `INSTALL_DIR`）：

```bash
# 以下所有方式都能正确工作
cd ~/.n2n-termux && ./start.sh
bash ~/.n2n-termux/start.sh
~/my-project/.n2n-termux/background.sh
cd /tmp && ~/.n2n-termux/stop.sh
```

---

## 🔄 核心逻辑：到底谁 Ping 谁？

组网系统中的设备分为两类：**TUN 模式**（PC / Android APK）和 **SOCKS5 模式**（Termux）。这决定了互访能力的差异。

### 互通能力矩阵

假设 Android/PC 的虚拟 IP 是 `10.64.0.3`，Termux 的虚拟 IP 是 `10.64.0.2`：

| 主动方 | 目标方 | TCP | UDP | ICMP (Ping) | 说明 |
|:---|:---|:---|:---|:---|:---|
| **PC ↔ Android** | | ✅ | ✅ | ✅ | 都是 TUN，完全透明 |
| **PC/Android → Termux** | | ✅ | ✅ | ✅ | Termux 内置 FWD 接管 |
| **Termux → PC/Android** | | ✅ | ❌ | ❌ | SOCKS5 的物理限制 |

### 为什么 Termux 不能主动 Ping 别人？

Termux 没有 root 权限，Android 内核不允许它创建 TUN 网卡或 raw socket。

1. **主动出站**：只能走 SOCKS5 代理（仅支持 TCP）。
2. **被动入站**：由内置的 `Packet Forwarder` 在用户态代为接收并回复 ICMP/UDP/TCP。

**结论：Termux 可以被完美 Ping 通，但无法主动去 Ping 别人。**

> **注**：如果 Termux 已 root 且有 `/dev/net/tun`，可以直接用 [n2n-go-client](https://github.com/goyo123321/n2n-go-client) 获得完整 TUN 能力。

---

## 🎯 终极验收：PC/Android 主动访问 Termux

得益于内置的 `Packet Forwarder`，**PC 和 Android (APK)** 开启 VPN 后，可以把 Termux 当作一台真实机器来访问，**无需任何代理**。

假设 Termux 的虚拟 IP 是 `10.64.0.3`。

### 步骤 1：在 Termux 启动服务

```bash
# 启动 TCP 服务 (监听 127.0.0.1 即可，FWD 会自动接管)
python3 -m http.server 8080 --bind 127.0.0.1 &

# 安装并启动 UDP 服务
pkg install socat -y
socat -v UDP-RECVFROM:5353,fork EXEC:cat &
```

### 步骤 2：在 PC/Android 端执行终极测试

```bash
# 1. ICMP 测试
ping 10.64.0.3

# 2. TCP 测试
curl http://10.64.0.3:8080/

# 3. UDP 测试
echo "hi" | nc -u 10.64.0.3 5353
```

**预期现象与日志：**

- `ping` 正常通，有 RTT 延迟，无丢包。
- `curl` 成功返回当前目录的 HTML 列表。
- `nc` 命令无卡顿直接退出。
- **Termux 端日志**会清晰打印出拦截与转发记录：

```text
[FWD-ICMP] Echo Request from 10.64.0.x
[FWD-TCP] + 10.64.0.x:xxxxx → 127.0.0.1:8080
[FWD-UDP] + 10.64.0.x:xxxxx → 127.0.0.1:5353
```

---

## 📡 反向测试：Termux 主动访问 PC/Android

由于 Termux 只能走 SOCKS5，主动访问其他端时，**必须显式配置代理**。假设目标端虚拟 IP 是 `10.64.0.3`。

1. **目标端（PC/Android）启动服务**：
   例如在目标端起个 HTTP 服务（监听 `0.0.0.0`）。

2. **在 Termux 端访问**：

```bash
curl --socks5 127.0.0.1:1080 http://10.64.0.3:8080/
```

---

## 🚀 快速开始

> ⚠️ **两个前置注意**：
>
> 1. **`SIGNALING_URL` 必须带 `wss://` 前缀**（或 `ws://` 用于本地调试）。漏写会报 `malformed ws or wss URL`。`install.sh` 会自动补全。
> 2. **安装目录默认是当前工作目录下的 `.n2n-termux`**，不再是 `~/.n2n-termux`。见 [📂 安装路径](#-安装路径默认当前目录)。

### 方式 A：一键安装脚本（推荐）

```bash
# 推荐方式：下载到任意位置，再用 bash 执行
curl -fsSL https://raw.githubusercontent.com/goyo123321/n2n-termux/main/install.sh -o ~/install_n2n_termux.sh
bash ~/install_n2n_termux.sh
```

> **不推荐** `bash <(curl ...)`：进程替换在某些 Termux 环境下失败会静默退出。下载后 `bash` 执行更稳定。

**安装位置**：`<当前工作目录>/.n2n-termux/`

**日常操作**：

```bash
# 前台启动（能看到日志，Ctrl+C 停）
~/.n2n-termux/start.sh

# 后台启动
~/.n2n-termux/background.sh

# 停止
~/.n2n-termux/stop.sh

# 看日志
tail -f ~/.n2n-termux/n2n.log

# 从任意目录调用都行
cd /tmp && ~/.n2n-termux/background.sh
```

> 上面的 `~/.n2n-termux` 假设你在 `~` 下安装了。如果你在 `/sdcard/n2n` 下安装，路径是 `/sdcard/n2n/.n2n-termux/`。

### 方式 B：静默后台安装

```bash
SIGNALING_URL="wss://<你的-worker-域名>" \
ROOM_ID="default-room" \
CLIENT_ID="termux-a" \
NODE_NAME="My-Termux" \
AUTO_START=1 \
bash ~/install_n2n_termux.sh
```

### 方式 C：手动下载运行

```bash
# 下载到当前位置（自己决定放哪）
curl -L -o ./n2n-termux \
  https://github.com/goyo123321/n2n-termux/releases/latest/download/n2n-termux-linux-arm64
chmod +x ./n2n-termux

# 运行
STUN_SERVERS="stun.chat.bilibili.com:3478,stun.hitv.com:3478" \
SIGNALING_URL="wss://<你的-worker-域名>" \
ROOM_ID="default-room" \
CLIENT_ID="termux-a" \
NODE_NAME="My-Termux" \
SOCKS5_LISTEN="127.0.0.1:1080" \
./n2n-termux
```

### 方式 D：后台运行（nohup）

```bash
nohup env \
  SIGNALING_URL="wss://<你的-worker-域名>" \
  ROOM_ID="default-room" \
  CLIENT_ID="termux-a" \
  NODE_NAME="My-Termux" \
  ./n2n-termux > ./n2n.log 2>&1 &

tail -f ./n2n.log
```

---

## 📦 二进制命名说明

从 **v1.1.21** 起，Release 二进制名带 `n2n-termux-` 前缀，与 `n2n-go-client` 区分：

| 项目 | 二进制名 |
|:---|:---|
| **n2n-termux** | `n2n-termux-linux-arm64` / `n2n-termux-linux-armv7` / `n2n-termux-linux-amd64` |
| n2n-go-client | `n2n-go-client-linux-amd64` / `n2n-go-client-linux-arm64` / ... |

**好处**：下载后一眼看出是哪个，两个项目放同一目录也不冲突，`install.sh` 的 pgrep 也不会误杀。

**历史版本**（v1.1.20 及之前）的二进制名是 `n2n-client-linux-*`，与 n2n-go-client 同名——如果还在用旧版，下载后手动改个名：

```bash
mv n2n-client-linux-arm64 n2n-termux
chmod +x n2n-termux
```

---

## ⚙️ 环境变量配置

| 变量 | 必填 | 默认值 | 说明 |
|:---|:---|:---|:---|
| `SIGNALING_URL` | ✅ | — | edge-signal 的 WSS 地址，**必须带 `wss://` 前缀** |
| `ROOM_ID` | ❌ | `default-room` | 房间名 |
| `CLIENT_ID` | ❌ | 自动生成并持久化 | 客户端唯一 ID，保存在 `$INSTALL_DIR/client_id` |
| `NODE_NAME` | ❌ | hostname | 显示名 |
| `SOCKS5_LISTEN` | ❌ | `127.0.0.1:1080` | SOCKS5 监听地址 |
| `CONNECT_TOKEN` | ❌ | `""` | 连接密码（Worker 配了才需要）|
| `STUN_SERVERS` | ❌ | 内置 | 自定义 STUN 服务器（逗号分隔）|
| `UDP_PORT` | ❌ | `0` | P2P UDP 端口，0=内核自动分配 |
| `INSTALL_DIR` | ❌ | `$(pwd)/.n2n-termux` | 安装目录，默认当前工作目录 |

---

## 🔧 LAN 优先机制

Termux 从 v1.1.21 起与 PC/Android 完全对齐，`Edge` 的 8 个 `UDPAddr` 写入点全部优先用 LAN：

| 写入点 | 触发时机 |
|:---|:---|
| `registerPeer` 新建 | 收到 `ready` / `joined` 时新建 peer |
| `registerPeer` 更新 | 收到 `joined` 更新已有 peer |
| `ensureTargetPeer` 新建 | 收到打洞指令时新建 peer |
| `ensureTargetPeer` 更新 | 收到打洞指令时更新已有 peer |
| `notePeerCommon` | 收到 Punch / ACK / 数据帧 |
| `handleP2PFrame` | 收到 N2NS 帧 |
| `recordP2PSuccess` | 打洞成功时 |
| `SendToPeer` | 每次发包时兜底 |

**效果**：同 WiFi 场景下，即使 `UDPAddr` 已被切成公网，发包时也会优先尝试 `LanEndpoints`，避免 CGNAT hairpin。

**修复前**：Android → Termux 的 ping RTT 50-70ms（回程走公网）
**修复后**：RTT 10-15ms（回程走 LAN）

---

## 🔌 N2NW / N2NA 二进制传输

WS 中继数据面使用两套二进制帧：

| 通道 | 帧格式 | 说明 |
|:---|:---|:---|
| UDP P2P / LAN | 裸 IPv4 | 直接 UDP，无封装 |
| UDP TURN | 裸 IPv4 | TURN relay 封装 |
| UDP N2NS | N2NS（21B 头）| P2P TCP stream + PING/PONG |
| UDP N2NP/N2NA | 4B magic | 打洞握手 |
| **WS 二进制 N2NW** | **6B 头 + peerId + IPv4** | WS 中继 IPv4 数据面 |
| **WS 二进制 N2NA** | **7B 头 + peerId + streamId + TCP 字节** | WS 中继 app_stream 数据面（新增） |
| WS 文本 app_stream_* | base64 JSON | 旧协议（自动降级时使用） |

**服务端按目标端 `features` 自动协商**：目标支持 `n2na` 走二进制，否则降级到文本。旧客户端完全兼容。

### N2NW 帧格式

```
  0    1    2    3    4     5      6..6+N        6+N..
┌────┬────┬────┬────┬─────┬──────┬─────────────┬────────────┐
│ 'N'│ '2'│ 'N'│ 'W'│flags│len(N)│ peerId(NB)  │  IPv4 包   │
└────┴────┴────┴────┴─────┴──────┴─────────────┴────────────┘
   客户端→服务端: peerId = 目标 clientId
   服务端→客户端: peerId = 发送方 clientId（服务端重写）
```

### N2NA 帧格式

```
  0    1    2    3    4     5      6..6+N      6+N      7+N..7+N+M      7+N+M..
┌────┬────┬────┬────┬─────┬──────┬─────────────┬───────┬──────────────┬────────────┐
│ 'N'│ '2'│ 'N'│ 'A'│flags│len(N)│ peerId(NB)  │len(M) │ streamId(MB) │ TCP 字节   │
└────┴────┴────┴────┴─────┴──────┴─────────────┴───────┴──────────────┴────────────┘
   客户端→服务端: peerId = 目标 clientId
   服务端→客户端: peerId = 发送方 clientId（服务端重写）
```

---

## 🔌 协议能力协商（features）

Termux 在 `p2p_metadata` 中上报 `features=["n2na","n2nw"]`，服务端据此决定 WS 中继的帧格式。

### 混合房间行为

服务端按**目标端**是否支持 `n2na` 决定：

| 发送方 | 接收方 | 服务端行为 |
|:---|:---|:---|
| 新 | 新 | 二进制转发（N2NA，节省 ~27% 带宽） |
| 新 | 旧 | 解码转 `app_stream_data` 文本 |
| 旧 | 新 | 文本（新客户端兼容） |
| 旧 | 旧 | 文本 |

**零丢包**——新旧客户端可在同一房间通信，服务端自动协商。

### 向后兼容保证

- 旧客户端不上报 `features` → 服务端视为 `[]` → 全走文本降级，**行为与升级前完全一致**
- 服务端**不需要等待任何客户端先升级**——部署后即生效
- 新旧客户端可以在同一房间共存，互不影响

### 验证 features 上报

```bash
curl -s "https://<worker>/api/admin/status/<room>?token=<ADMIN_TOKEN>" | jq '.peers[] | {code, features}'
```

新客户端返回 `["n2na","n2nw"]`，旧客户端返回 `[]`。

---

## 🆕 版本行为变化说明

### 端口漂移保留 P2P 状态（P0 修复）

**行为变化**：从最新版本起，**只有出口 IP 变化时才清空连接状态**，端口漂移（HardNAT 常见）时保留 P2P 状态。

**为什么改**：

旧版 Termux（与 PC 一致）在收到 `joined`/`ready` 时，只要 `pubSocket` 变化就无条件 `ClearPeer`。HardNAT 下端口漂移频繁，导致每次 `joined`/`ready` 都清空 P2P 状态，P2P 通道反复断开重连。

**新行为**（与服务端 `room.ts`、Android 端对齐）：

- **IP 变化** → 清空连接状态，重新打洞
- **仅端口漂移** → 保留 P2P 状态，P2P 通道通常仍可用

**日志表现**：

```
[NAT-HOLE] xxx 出口 IP 变化 1.2.3.4 → 5.6.7.8，清空连接状态 (was=1)
[NAT-HOLE] xxx 端口漂移 1.2.3.4:5000 → 1.2.3.4:5001，保留 P2P 状态 (was=0)
```

### 重连清理更彻底（P1 修复）

**行为变化**：WS 重连成功后，除了清理 `failCounts`，还会清理 `natHoleActive` 中的 in-flight 标记。

**为什么改**：旧版重连后，残留的 `targetMac@rung` 标记会导致后续打洞指令被误判为“已在处理中”而跳过，直到该标记因超时被清除。

**新行为**：重连时同时清空 `failCounts` 和 `natHoleActive`，重连后打洞指令立即正常执行。

**日志表现**：

```
[NAT-HOLE] WS 重连，清空 2 个失败计数 / 1 个 in-flight 标记
```

### `_reconnected` 消息真正触发（P1 修复）

**行为变化**：WS 重连成功后，客户端会显式发送 `_reconnected` 消息，触发 `handleSignaling` 里的完整清理逻辑。

**为什么改**：旧版 `ws_transport.go` 重连成功后只调用 `onReconnect` 回调，**不发送 `_reconnected` 消息**，导致 `handleSignaling` 里的 `if t == "_reconnected"` 分支永远是死代码。

**新行为**：重连成功后调用 `ws.routeText({type:"_reconnected"})`，触发完整的清理流程（与 `onReconnect` 幂等）。

---

## 📋 启动日志

启动成功后：

```text
n2n-termux dev 启动
[配置] 未设置 CONNECT_TOKEN
[P2P] UDP 监听端口 54321
[NAT] 启动预收集 LAN: [192.168.10.10:54321]
[NAT] STUN 74.125.250.129:19302 → x.x.x.x:19190
[NAT] HardNAT（对称），ports_diff=0 pub=x.x.x.x:19190
[WS] 连接 wss://<你的-worker-域名>/ws/default-room?cid=termux-a
已连接信令，Client ID: termux-a，节点名: My-Termux
分配虚拟 IP: 10.64.0.2（网段 10.64.0.0/24）
[NAT] 上报 LAN 地址: [192.168.10.10:54321]
[信令] 上报 p2p_metadata: natType=HardNAT publicEndpoint="x.x.x.x:19190" multiExit=true assisted=2 lan=1 features=[n2na,n2nw]
[Keepalive] 启动，每 5s 刷新 3 个 STUN 服务器
[P2P-Keepalive] 启动，每 15s 刷新 P2P 通道
[SOCKS5] 监听 127.0.0.1:1080（用 curl --socks5 或 ALL_PROXY 访问虚拟 IP）
[FWD] 已启动（TCP 中继 + UDP 转发 + ICMP 响应）

================= 本机信息 =================
  Client ID   : termux-a
  节点名       : My-Termux
  虚拟 IP     : 10.64.0.2
  虚拟网段    : 10.64.0.0/24
  P2P 端口    : 54321
  模式         : SOCKS5 + packet-forwarder（双向互通）
==========================================
```

**关键检查点**：

| 日志 | 含义 |
|:---|:---|
| `[NAT] 上报 LAN 地址: [...]` 非空 | LAN 收集成功 |
| `[SOCKS5] 监听 127.0.0.1:1080` | **Termux 独有**（不是 PC 版） |
| `[FWD] 已启动` | Packet Forwarder 启动 |
| `[WS] 连接 wss://...` | URL 带了 wss://（不是 `malformed`） |
| `[信令] 上报 p2p_metadata: ... features=[n2na,n2nw]` | 能力上报成功 |
| `[P2P] xxx 新建 peer，初始 UDPAddr 用 LAN: ...` | LAN 优先生效 |

---

## ❓ 常见问题

### Q: 启动报错 `No such file or directory`？

**原因**：路径写错了。**安装目录现在是 `<当前工作目录>/.n2n-termux`**，不再是 `~/.n2n-termux`。

**快速判断**：

```bash
# 到之前运行的目录找
ls -la .n2n-termux/n2n-termux

# 或者全局搜
find ~ -name "n2n-termux" -type f 2>/dev/null
find /sdcard -name "n2n-termux" -type f 2>/dev/null
```

**建议**：安装时看到脚本输出里的 `安装目录: /实际/绝对/路径/.n2n-termux`，直接复制那个路径。

### Q: 启动报错 `连接信令失败: malformed ws or wss URL`？

**原因**：`SIGNALING_URL` 漏了 `wss://` 前缀。

**修复**：

```bash
# 编辑 config.env
sed -i 's|^SIGNALING_URL=.*|SIGNALING_URL="wss://<你的-worker-域名>"|' \
  ./.n2n-termux/config.env

# 确认
grep SIGNALING_URL ./.n2n-termux/config.env

# 重新启动
./.n2n-termux/start.sh
```

**格式说明**：

| 输入 | 结果 |
|:---|:---|
| `wss://your-worker.example.com` | ✅ |
| `ws://localhost:8080` | ✅ |
| `https://your-worker.example.com` | ⚠️ 会被自动转成 `wss://` |
| `your-worker.example.com` | ❌ 缺前缀（install.sh 会自动补） |
| `wss://your-worker.example.com/ws/default-room` | ❌ 不要带路径 |

### Q: 启动报错 `tls: failed to verify certificate: x509: certificate signed by unknown authority`？

**原因**：Termux 没有系统 CA 证书路径，Go 标准库无法验证 WSS 证书。

**解决**：

```bash
pkg install ca-certificates
```

`install.sh` 会在安装时自动检查并安装。

### Q: 为什么 PC 能 ping 通 Termux，但 Termux 不能 ping 通 PC？

**原因**：Termux 走的是 SOCKS5 代理，而 SOCKS5 协议本身不支持 ICMP 和 UDP。

**解决**：这是无 root 权限的底层限制。如果需要 Termux 具备主动 Ping 能力：

- **手机已 root** → 用 [n2n-go-client](https://github.com/goyo123321/n2n-go-client)（TUN 模式）
- **手机无 root** → 只能被动接受 ping，不能主动 ping

### Q: Android → Termux ping 延迟 50-70ms？

**原因**：**v1.2.1 之前** Termux 的 `SendToPeer` 缺 LAN 优先，回程走公网 hairpin。

**修复**：升级到 v1.2.1+，日志里应该能看到：

```text
[P2P] xxx 新建 peer，初始 UDPAddr 用 LAN: 192.168.10.12:59566
```

**验证**：

```bash
# PC/Android ping Termux
ping -i 0.2 -c 100 10.64.0.2
```

**预期**：`rtt min/avg/max/mdev = 4.x/10.x/30.x/5.x ms`。

### Q: 下载的二进制名字是 `n2n-client-linux-arm64`，不是 `n2n-termux-linux-arm64`？

**原因**：你下载的是 **v1.2.0 及之前**的历史版本。

**解决**：

- **升级到 v1.2.1+**：二进制名变成 `n2n-termux-linux-*`
- **或者手动改名**：`mv n2n-client-linux-arm64 n2n-termux`

### Q: 一键安装脚本卡住了或没有任何输出？

**原因**：
- `bash <(curl ...)` 的进程替换在某些 Termux 环境下失败
- `/tmp` 不可写（`curl: (23) client returned ERROR on write`）
- DNS 指向 `[::1]:53`

**解决**：**下载后执行**，不要用 `bash <(curl ...)`：

```bash
# 下载到 HOME（避开 /tmp）
curl -fsSL https://raw.githubusercontent.com/goyo123321/n2n-termux/main/install.sh -o ~/install_n2n_termux.sh

# 用 bash -x 调试
bash -x ~/install_n2n_termux.sh
```

### Q: 如何确认二进制是 Termux 版而不是 PC 版？

**方法 1：看启动日志**

| 日志 | 判断 |
|:---|:---|
| `[SOCKS5] 监听 127.0.0.1:1080` + `[FWD] 已启动` | ✅ Termux 版 |
| `[TUN] n2n0 已启动` | ❌ PC 版（跑错平台了） |

**方法 2：看文件名**

```bash
ls -lh ./.n2n-termux/n2n-termux      # ✅ Termux 版
ls -lh ./.n2n-go/n2n-go-client        # PC 版
```

**方法 3：strings 检查**

```bash
strings ./.n2n-termux/n2n-termux | grep -i "termux\|packet-forwarder" | head -3
```

### Q: 息屏后 ping 有 200-500ms 尖峰？

**原因**：Android WiFi 芯片的 PS-Poll 省电模式，与 n2n 无关。

**解决**：

1. **路由器关闭 U-APSD**（华硕/梅林/OpenWrt）
2. **切 5GHz 频段**（2.4G 干扰更多）
3. **DTIM 周期改 1**
4. **Android 电池优化白名单**

**Termux 端本身没有 WiFi 控制权**，只能靠路由器侧优化。

### Q: 日志在哪？

**install.sh 装的**：

```bash
tail -f ./.n2n-termux/n2n.log
```

**手动下载的**：

```bash
tail -f ./n2n.log      # 取决于你重定向到哪
```

### Q: 如何更新？

```bash
# 下载最新 install.sh
curl -fsSL https://raw.githubusercontent.com/goyo123321/n2n-termux/main/install.sh -o ~/install_n2n_termux.sh

# 到你之前安装的目录
cd /你之前安装的目录

# 运行更新（会自动用当前目录的 .n2n-termux）
bash ~/install_n2n_termux.sh

# 只检查不下载
CHECK_ONLY=1 bash ~/install_n2n_termux.sh

# 强制重装
FORCE_UPDATE=1 bash ~/install_n2n_termux.sh
```

### Q: 如何卸载？

```bash
# 停止进程
./.n2n-termux/stop.sh 2>/dev/null

# 删除安装目录
rm -rf ./.n2n-termux

# 如果之前在 ~ 下安装的
rm -rf ~/.n2n-termux
```

### Q: 之前装在 `~/.n2n-termux`，现在装在 `$(pwd)/.n2n-termux`，怎么迁移？

```bash
# 1. 停止旧进程
~/.n2n-termux/stop.sh 2>/dev/null

# 2. 在你想要的目录下创建
cd ~/my-project
mkdir -p .n2n-termux

# 3. 拷贝配置和 client_id
cp ~/.n2n-termux/config.env ./.n2n-termux/ 2>/dev/null || true
cp ~/.n2n-termux/client_id ./.n2n-termux/ 2>/dev/null || true

# 4. 重新运行 install.sh（版本相同会跳过下载）
bash ~/install_n2n_termux.sh

# 5. 确认 client_id 未变
diff ~/.n2n-termux/client_id ./.n2n-termux/client_id && echo "ID 不变 ✓"

# 6. 删除旧目录
rm -rf ~/.n2n-termux
```

`CLIENT_ID` 不变，虚拟 IP 保持稳定。

### Q: 从任意目录调用 `start.sh` / `background.sh` / `stop.sh` 会不会找错配置文件？

**不会**。三个脚本里都用**自身所在目录推导 `INSTALL_DIR`**：

```bash
if [ -z "${INSTALL_DIR:-}" ]; then
    INSTALL_DIR="$(cd "$(dirname "$0")" && pwd)"
fi
```

所以从任意目录调用都能正确找到 `config.env` 和 `n2n-termux`。

### Q: 混合房间（新旧客户端共存）会丢数据吗？

**不会**。

服务端按目标端 `features` 自动协商：

| 发送方 | 接收方 | 数据面 |
|:---|:---|:---|
| 新 Termux | 新 PC / 新 Android | N2NA 二进制 |
| 新 Termux | 旧客户端 | 文本 `app_stream_data` |
| 旧客户端 | 新 Termux | 文本（新 Termux 兼容） |

**旧客户端完全无感知**，新 Termux 自动获得二进制优化。

### Q: 管理面板为什么显示 `无能力上报`？

**旧客户端**（升级 features 之前的版本）不上报 `features` 字段。

- **不影响功能**：服务端自动降级到文本 `app_stream_data`，连接正常
- **升级后消失**：升级到最新版本后，管理面板会显示 `n2na` / `n2nw` chips

### Q: 单向 P2P 场景（A→B 通，B→A 断）会怎样？

**Termux 与 PC 一致：保持 P2PAvailable，60 秒后自动降级到 TURN/WS，不会卡在 P2P 丢包。**

**原因**：Termux 收到 PING 只更新活跃时间，**只有收到 PONG 或数据帧才升级 P2P**。单向 P2P 场景下保持 `ConnP2PAvailable`，60 秒后 `scheduleP2PAvailTimeout` 触发 `ForceFallback`。

**日志表现**：

```
[P2P] 收到 PONG → P2P 直连（双向通道已验证）    ← 正常
[P2P] xxx P2PAvailable 超时 60s 未验证数据面，强制降级   ← 单向 P2P
```

---

## 🔗 相关项目

| 项目 | 平台 | 模式 | 互通 |
|:---|:---|:---|:---|
| [edge-signal](https://github.com/goyo123321/edge-signal) | Cloudflare Workers | 信令服务器 | — |
| [n2n-go-client](https://github.com/goyo123321/n2n-go-client) | Linux / macOS / Windows / 路由器 | TUN | ✅ 全部 |
| **n2n-termux** | Android Termux | SOCKS5 + FWD | ✅ 全部 |
| [n2n-android](https://github.com/goyo123321/n2n-android) | Android APK | VpnService | ✅ 全部 |

---

## 📄 License

MIT
