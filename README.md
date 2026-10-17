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
- **N2NW 二进制** — WS 中继数据面用二进制帧，比 base64 省 33% 带宽
- **轻量无依赖** — 纯 Go 标准库，无需 gVisor，编译仅需 2 秒，二进制 ~10MB
- **UDP 保活** — 每 5 秒刷新 STUN 映射，防止 CGNAT 端口漂移
- **P2P 通道保活** — 每 15 秒往对端发 N2NS PING 多目标探针
- **零配置网段** — 服务端通过 `VIRTUAL_NETWORK` 下发，客户端自动适配
- **一键部署** — `install.sh` 自动下载 + 配置 + 启动

---

## 📂 二进制路径速查（⚠️ 最容易踩的坑）

**两种安装方式，二进制路径完全不同**，先看清楚再启动：

| 安装方式 | 二进制位置 | 启动命令 |
|:---|:---|:---|
| **方式 A：`install.sh` 自动安装** | `~/.n2n-termux/n2n-termux`<br>（**带点的隐藏目录**） | `~/.n2n-termux/start.sh` |
| **方式 B：手动下载** | `~/n2n-termux`<br>（`curl -o ~/n2n-termux` 下载到的位置） | `~/n2n-termux` |

**常见错误对照**：

```bash
# ❌ 错误：install.sh 装的，却用 ~/n2n-termux 启动
~/n2n-termux
# bash: /data/data/com.termux/files/home/n2n-termux: No such file or directory

# ✅ 正确：install.sh 装的用带点目录
~/.n2n-termux/n2n-termux
# 或直接
~/.n2n-termux/start.sh

# ✅ 正确：手动下载到 ~/ 的用这个
~/n2n-termux
```

**验证二进制在哪**：

```bash
ls -lh ~/.n2n-termux/n2n-termux    # install.sh 装的（存在 → 用这个）
ls -lh ~/n2n-termux                # 手动下载的（存在 → 用这个）
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

在 PC 或开启了 VPN 的 Android 终端上直接运行：

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

*(如果你用 `curl` 卡住或 `nc` 超时，请检查 Termux 上的服务是否真的启动成功了。)*

---

## 📡 反向测试：Termux 主动访问 PC/Android

**由于 Termux 只能走 SOCKS5，主动访问其他端时，必须显式配置代理。** 假设目标端虚拟 IP 是 `10.64.0.3`。

1. **目标端（PC/Android）启动服务**：
   例如在目标端起个 HTTP 服务（监听 `0.0.0.0`）。

2. **在 Termux 端访问**：

```bash
# 必须指定 --socks5
curl --socks5 127.0.0.1:1080 http://10.64.0.3:8080/
```

---

## 🚀 快速开始

> ⚠️ **两个前置注意**：
>
> 1. **`SIGNALING_URL` 必须带 `wss://` 前缀**（或 `ws://` 用于本地调试）。漏写会报 `malformed ws or wss URL`。**v1.1.23+ 的 `install.sh` 会自动补全**。
>
> 2. **二进制路径**：`install.sh` 装的是 `~/.n2n-termux/n2n-termux`（带点的隐藏目录）；手动下载的是 `~/n2n-termux`。详见 [📂 二进制路径速查](#-二进制路径速查-最容易踩的坑)。

### 方式 A：一键安装脚本（推荐）

```bash
# 交互式安装（会提示填写配置）
SIGNALING_URL="wss://<你的-worker-域名>" \
ROOM_ID="default-room" \
CLIENT_ID="termux-a" \
NODE_NAME="My-Termux" \
bash <(curl -fsSL https://raw.githubusercontent.com/goyo123321/n2n-termux/main/install.sh)
```

**安装位置**：`~/.n2n-termux/n2n-termux`

**日常操作**：

```bash
~/.n2n-termux/start.sh          # 前台启动
~/.n2n-termux/background.sh     # 后台启动
~/.n2n-termux/stop.sh           # 停止
tail -f ~/.n2n-termux/n2n.log   # 看日志
```

### 方式 B：静默后台安装

```bash
SIGNALING_URL="wss://<你的-worker-域名>" \
ROOM_ID="default-room" \
CLIENT_ID="termux-a" \
NODE_NAME="My-Termux" \
AUTO_START=1 \
bash <(curl -fsSL https://raw.githubusercontent.com/goyo123321/n2n-termux/main/install.sh)
```

**安装位置**：同方式 A

### 方式 C：手动下载运行（最稳定）

Termux 网络经常导致一键脚本卡住，推荐直接手动下载：

```bash
# 1. 下载到 HOME 目录（注意这里是 ~/n2n-termux，不是 ~/.n2n-termux/）
curl -L -o ~/n2n-termux \
  https://github.com/goyo123321/n2n-termux/releases/latest/download/n2n-termux-linux-arm64

# 2. 给执行权限
chmod +x ~/n2n-termux

# 3. 运行（SIGNALING_URL 必须有 wss://）
STUN_SERVERS="stun.chat.bilibili.com:3478,stun.hitv.com:3478" \
SIGNALING_URL="wss://<你的-worker-域名>" \
ROOM_ID="default-room" \
CLIENT_ID="termux-a" \
NODE_NAME="My-Termux" \
SOCKS5_LISTEN="127.0.0.1:1080" \
~/n2n-termux
```

**安装位置**：`~/n2n-termux`（**没有点，直接在 home 下**）

> ⚠️ **注意**：这种方式下载的二进制是 `~/n2n-termux`，和 install.sh 的 `~/.n2n-termux/n2n-termux` **不是同一个位置**。不要混用。
>
> ⚠️ `SIGNALING_URL` 必须带 `wss://`，否则报 `malformed ws or wss URL`。

### 方式 D：手动下载 + 后台运行

```bash
nohup env \
  SIGNALING_URL="wss://<你的-worker-域名>" \
  ROOM_ID="default-room" \
  CLIENT_ID="termux-a" \
  NODE_NAME="My-Termux" \
  ~/n2n-termux > ~/n2n.log 2>&1 &

tail -f ~/n2n.log
```

---

## 📦 二进制命名说明

从 **v1.2.1** 起，Release 二进制名带 `n2n-termux-` 前缀，与 `n2n-go-client` 区分：

| 项目 | 二进制名 |
|:---|:---|
| **n2n-termux** | `n2n-termux-linux-arm64` / `n2n-termux-linux-armv7` / `n2n-termux-linux-amd64` |
| n2n-go-client | `n2n-go-client-linux-amd64` / `n2n-go-client-linux-arm64` / ... |

**好处**：下载后一眼看出是哪个，两个项目放同一目录也不冲突。

**历史版本**（v1.2.0 及之前）的二进制名是 `n2n-client-linux-*`，与 n2n-go-client 同名——如果还在用旧版，下载后手动改个名：

```bash
mv n2n-client-linux-arm64 n2n-termux
chmod +x n2n-termux
```

---

## ⚙️ 环境变量配置

| 变量 | 必填 | 默认值 | 说明 |
|:---|:---|:---|:---|
| `SIGNALING_URL` | ✅ | — | edge-signal 的 WSS 地址，**必须带 `wss://` 前缀**（或 `ws://`） |
| `ROOM_ID` | ❌ | `default-room` | 房间名 |
| `CLIENT_ID` | ❌ | 自动生成并持久化 | 客户端唯一 ID，保存在 `~/.n2n-termux/client_id` |
| `NODE_NAME` | ❌ | hostname | 显示名 |
| `SOCKS5_LISTEN` | ❌ | `127.0.0.1:1080` | SOCKS5 监听地址 |
| `CONNECT_TOKEN` | ❌ | `""` | 连接密码（Worker 配了才需要）|
| `STUN_SERVERS` | ❌ | 内置 | 自定义 STUN 服务器（逗号分隔）|
| `UDP_PORT` | ❌ | `0` | P2P UDP 端口，0=内核自动分配 |
| `INSTALL_DIR` | ❌ | `~/.n2n-termux` | install.sh 的安装目录 |

---

## 🔧 LAN 优先机制

Termux 从 v1.2.1 起与 PC/Android 完全对齐，`Edge` 的 8 个 `UDPAddr` 写入点全部优先用 LAN：

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

## 🔌 N2NW 二进制传输

WS 中继数据面使用 **N2NW 二进制帧**（6 字节头 + peerId + 裸 IPv4 包），而非 base64 编码：

| 通道 | 帧格式 | 说明 |
|:---|:---|:---|
| UDP P2P / LAN | 裸 IPv4 | 直接 UDP，无封装 |
| UDP TURN | 裸 IPv4 | TURN relay 封装 |
| UDP N2NS | N2NS（21B 头）| P2P TCP stream + PING/PONG |
| UDP N2NP/N2NA | 4B magic | 打洞握手 |
| **WS 二进制 N2NW** | **6B 头 + peerId + IPv4** | WS 中继数据面 |
| WS 文本 app_stream_* | base64 JSON | SOCKS5 TCP 隧道（保持不变） |

**帧格式**：

```
  0    1    2    3    4     5      6..6+N        6+N..
┌────┬────┬────┬────┬─────┬──────┬─────────────┬────────────┐
│ 'N'│ '2'│ 'N'│ 'W'│flags│len(N)│ peerId(NB)  │  IPv4 包   │
└────┴────┴────┴────┴─────┴──────┴─────────────┴────────────┘
   客户端→服务端: peerId = 目标 clientId
   服务端→客户端: peerId = 发送方 clientId（服务端重写）
```

**⚠️ 兼容性**：三端客户端和 edge-signal 服务端**必须同步升级**。旧服务端遇到 N2NW magic 会静默丢弃（`data[0]>>4 != 4`），导致 WS 中继数据面不通。

---

## 📋 启动日志

启动成功后应该看到：

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
[信令] 上报 p2p_metadata: natType=HardNAT publicEndpoint="..." multiExit=true assisted=2 lan=1
[NAT] 上报 LAN 地址: [192.168.10.10:54321]
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
| `[NAT] 上报 LAN 地址: [...]` 非空 | LAN 收集成功（LAN 优先前提） |
| `[SOCKS5] 监听 127.0.0.1:1080` | Termux 独有（不是 PC 版） |
| `[FWD] 已启动` | Packet Forwarder 启动 |
| `[P2P] xxx 新建 peer，初始 UDPAddr 用 LAN: ...` | LAN 优先修复生效 |
| `[WS] 连接 wss://...` | **URL 带了 wss://**（不是 `malformed`） |

---

## 📁 目录结构

`install.sh` 安装后的目录：

```
~/.n2n-termux/
├── n2n-termux           # 二进制（手动下载的则在 ~/n2n-termux）
├── .version             # 当前版本号
├── config.env           # 配置（权限 600）
├── client_id            # 持久化的 Client ID
├── start.sh             # 前台启动脚本（Ctrl+C 停）
├── background.sh        # 后台启动脚本（nohup）
├── stop.sh              # 停止脚本
├── n2n.pid              # 后台运行时的 PID
└── n2n.log              # 日志（>5MB 自动备份到 n2n.log.old）
```

---

## ❓ 常见问题

### Q: 启动报错 `No such file or directory`，找不到二进制？

**原因**：**路径写错了**。`install.sh` 装的和手动下载的路径完全不同。

**快速判断**：

```bash
# 用 install.sh 装的 → 二进制在 ~/.n2n-termux/n2n-termux
ls -lh ~/.n2n-termux/n2n-termux

# 手动下载的 → 二进制在 ~/n2n-termux
ls -lh ~/n2n-termux
```

**哪个存在就用哪个**：

```bash
# install.sh 装的
~/.n2n-termux/n2n-termux
# 或直接
~/.n2n-termux/start.sh

# 手动下载的
~/n2n-termux
```

**详细对照**：

| 场景 | 错误命令 | 正确命令 |
|:---|:---|:---|
| install.sh 装完，手动跑 | `~/n2n-termux` | `~/.n2n-termux/n2n-termux` |
| install.sh 装完，正常启动 | — | `~/.n2n-termux/start.sh` |
| 手动下载到 home | `~/.n2n-termux/n2n-termux` | `~/n2n-termux` |

### Q: 启动报错 `连接信令失败: malformed ws or wss URL`？

**原因**：`SIGNALING_URL` 漏了 `wss://`（或 `ws://`）协议前缀。

**日志特征**：

```
[WS] 连接 <你的-worker-域名>/ws/default-room?cid=...
连接信令失败: malformed ws or wss URL
```

注意日志里是 `<域名>/ws/...`（没有 `wss://` 前缀）。

**修复方式 1：手动改 config.env**

```bash
# 把 SIGNALING_URL 改成带 wss:// 的
sed -i 's|^SIGNALING_URL=.*|SIGNALING_URL="wss://<你的-worker-域名>"|' \
  ~/.n2n-termux/config.env

# 确认
grep SIGNALING_URL ~/.n2n-termux/config.env
```

**修复方式 2：用 v1.1.23+ 的 install.sh**（自动补全）

```bash
# 输入不带 wss:// 的地址会自动补全
SIGNALING_URL="<你的-worker-域名>" bash <(curl -fsSL .../install.sh)
# 输出: 自动补全协议头: wss://
#       SIGNALING_URL = wss://<你的-worker-域名>
```

**正确的 SIGNALING_URL 格式**：

| 输入 | 结果 |
|:---|:---|
| `wss://your-worker.example.com` | ✅ 正确 |
| `ws://localhost:8080` | ✅ 正确（本地调试） |
| `https://your-worker.example.com` | ⚠️ 会被自动转成 `wss://` |
| `your-worker.example.com` | ❌ 缺前缀（新版 install.sh 自动补全） |
| `wss://your-worker.example.com/ws/default-room` | ❌ 不要带路径，只填域名 |

### Q: 启动报错 `tls: failed to verify certificate: x509: certificate signed by unknown authority`？

**原因**：Termux 没有系统 CA 证书路径，Go 标准库无法验证 WSS 证书。

**解决**：最新版（v1.2.1+）已内置自动探测机制，会从 `$PREFIX/etc/tls/cert.pem` 自动加载。如果仍然报错：

```bash
pkg install ca-certificates
```

`install.sh` 也会在安装时自动检查并安装 `ca-certificates`。

### Q: 为什么 PC 能 ping 通 Termux，但 Termux 不能 ping 通 PC？

**原因**：Termux 走的是 SOCKS5 代理，而 SOCKS5 协议本身不支持 ICMP 和 UDP。

**解决**：这是无 root 权限的底层限制。如果需要 Termux 具备主动 Ping 能力，请使用拥有 root 权限的 Linux 设备运行 [n2n-go-client](https://github.com/goyo123321/n2n-go-client)。

### Q: 为什么 `ping 10.64.0.x` 会延迟 200ms 左右？

**原因**：如果打洞失败降级到 WS 中继，ICMP 包会走 Cloudflare Worker 转发，延迟会升高。但数据不会丢失。

**解决**：正常现象，TUN 打洞成功后会自动降回 LAN/P2P，延迟降至 10ms 以内。

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

**预期**：`rtt min/avg/max/mdev = 4.x/10.x/30.x/5.x ms`（修复前 avg 是 50-70ms）。

### Q: 下载的二进制名字是 `n2n-client-linux-arm64`，不是 `n2n-termux-linux-arm64`？

**原因**：你下载的是 **v1.2.0 及之前**的历史版本，旧版二进制名与 `n2n-go-client` 同名。

**解决**：

- **升级到 v1.2.1+**：二进制名变成 `n2n-termux-linux-*`
- **或者手动改名**：`mv n2n-client-linux-arm64 n2n-termux`

### Q: 一键安装脚本卡住了？

**原因**：Termux 的网络环境（尤其是国内）经常导致 `curl | bash` 超时。

**解决**：直接手动下载二进制（注意路径是 `~/n2n-termux`，不是 `~/.n2n-termux/n2n-termux`）：

```bash
curl -L -o ~/n2n-termux \
  https://github.com/goyo123321/n2n-termux/releases/latest/download/n2n-termux-linux-arm64
chmod +x ~/n2n-termux

SIGNALING_URL="wss://<你的-worker-域名>" \
ROOM_ID="default-room" \
CLIENT_ID="termux-a" \
~/n2n-termux
```

### Q: 如何确认二进制是 Termux 版而不是 PC 版？

**方法 1：看启动日志**

| 日志 | 判断 |
|:---|:---|
| `[SOCKS5] 监听 127.0.0.1:1080` + `[FWD] 已启动` | ✅ Termux 版 |
| `[TUN] n2n0 已启动` | ❌ PC 版（跑错平台了） |

**方法 2：看文件名**

```bash
ls -lh ~/.n2n-termux/n2n-termux    # ✅ Termux 版（install.sh 装的）
ls -lh ~/n2n-termux                # ✅ Termux 版（手动下载的）
ls -lh ~/n2n-go-client             # PC 版
```

**方法 3：strings 检查**

```bash
strings ~/.n2n-termux/n2n-termux | grep -i "termux\|packet-forwarder" | head -3
```

### Q: 息屏后 ping 有 200-500ms 尖峰？

**原因**：Android WiFi 芯片的 PS-Poll 省电模式，与 n2n 无关。即使 PC 直连网关也会有相同尖峰。

**解决**：

1. **确认 WiFi 锁生效**（仅 Android APK 端，Termux 端无 WiFi 控制权）
2. **路由器关闭 U-APSD**（华硕/梅林/OpenWrt 都有这个设置）
3. **切 5GHz 频段**（2.4G 干扰更多）
4. **DTIM 周期改 1**（路由器无线高级设置）
5. **Android 电池优化白名单**（设置 → 应用 → 电池 → 不受限制）

**代码侧已经做到极限**——剩下的纯属 WiFi 物理层限制。

### Q: 日志在哪？

**install.sh 装的**：

```bash
tail -f ~/.n2n-termux/n2n.log
```

**手动下载的**：

```bash
tail -f ~/n2n.log
```

**前台运行的**：直接看终端输出。

### Q: 如何更新？

```bash
# 自动查最新版并更新（install.sh 装的）
bash <(curl -fsSL https://raw.githubusercontent.com/goyo123321/n2n-termux/main/install.sh)

# 只检查不下载
CHECK_ONLY=1 bash <(curl -fsSL https://raw.githubusercontent.com/goyo123321/n2n-termux/main/install.sh)

# 强制重装当前版本
FORCE_UPDATE=1 bash <(curl -fsSL https://raw.githubusercontent.com/goyo123321/n2n-termux/main/install.sh)

# 指定版本
VERSION=v1.2.1 bash <(curl -fsSL https://raw.githubusercontent.com/goyo123321/n2n-termux/main/install.sh)
```

**手动下载的**：重新下载覆盖 `~/n2n-termux` 即可。

### Q: 如何卸载？

**install.sh 装的**：

```bash
~/.n2n-termux/stop.sh 2>/dev/null
rm -rf ~/.n2n-termux
```

**手动下载的**：

```bash
pkill -f "n2n-termux"
rm ~/n2n-termux
rm ~/n2n.log
```

---

## 🔗 相关项目

| 项目 | 平台 | 模式 | 互通 |
|:---|:---|:---|:---|
| [edge-signal](https://github.com/goyo123321/edge-signal) | Cloudflare Workers | 信令服务器 | — |
| [n2n-go-client](https://github.com/goyo123321/n2n-go-client) | Linux / macOS / Windows | TUN | ✅ 全部 |
| **n2n-termux** | Android Termux | SOCKS5 + FWD | ✅ 全部 |
| [n2n-android](https://github.com/goyo123321/n2n-android) | Android APK | VpnService | ✅ 全部 |

---

## 📄 License

MIT
