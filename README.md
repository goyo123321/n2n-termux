# n2n-termux

在 **Android Termux** 里跑的点对点组网客户端。**无需 root，无需 TUN**，通过 **SOCKS5 代理**把应用层流量接入 n2n 虚拟网。

配合 [edge-signal](https://github.com/goyo123321/edge-signal) 使用，与 [n2n-go-client](https://github.com/goyo123321/n2n-go-client) 和 [n2n-android](https://github.com/goyo123321/n2n-android) 互通。

---

## ✨ 特性

- **无 root** — Termux 普通 App 权限即可运行
- **无 TUN** — 不碰 `/dev/net/tun`，用 SOCKS5 代理接入
- **三级降级** — `P2P → TURN → WS`，连接永远可用
- **P2P 直连** — UDP 打洞 + 自定义可靠流协议（`N2NS` 帧），延迟 10-50ms
- **TURN 中继** — 手写 TURN-Lite 客户端，UDP 不通自动切 TCP
- **WS 中继兜底** — 通过 Cloudflare Worker 转发，不消耗额外资源
- **DNS 内置修复** — 解决 Go 静态二进制读不到 Termux `/etc/resolv.conf` 的问题
- **多架构** — arm64 / armv7 / amd64 三种二进制
- **一键安装** — `curl | bash` 式脚本，全自动配置

---

## 🏗️ 架构

Termux 是普通 App，Android 内核**不允许**其创建 TUN 设备，所以不能像 PC / Android APK 那样接管系统路由。

替代方案：**用 SOCKS5 代理截获应用层 TCP 连接**，通过自定义帧协议在对端重建连接。

```
┌────────────────────────────────────────────────────────┐
│  Termux 里的应用（curl / ssh / wget / …）              │
│      │                                                  │
│      │ SOCKS5 请求 "连 100.64.0.3:8080"                │
│      ▼                                                  │
│  ┌──────────────┐    ┌────────────────┐                │
│  │ SOCKS5 服务器 │───▶│ P2P 可靠流 /    │                │
│  │ socks5.go    │    │ WS 应用流       │                │
│  └──────────────┘    └────────┬───────┘                │
│                               │                         │
│                     三级数据通路：                       │
│                     ├─ P2P UDP（10-50ms）              │
│                     ├─ TURN 中继（30-100ms）           │
│                     └─ WebSocket（100-300ms）           │
└───────────────────────────────┬────────────────────────┘
                                │
                                ▼
                    ┌───────────────────────┐
                    │ edge-signal (Worker)  │
                    │ ├─ 信令路由            │
                    │ ├─ NAT 打洞协调        │
                    │ ├─ 下发 TURN 凭证      │
                    │ └─ WS 中继兜底         │
                    └───────────────────────┘
                                │
                                ▼
                    ┌───────────────────────┐
                    │  对端客户端            │
                    │  n2n-termux /         │
                    │  n2n-go-client /      │
                    │  n2n-android          │
                    └───────────────────────┘
```

**数据面格式**：

| 通道 | 帧格式 | 传输 |
|------|--------|------|
| P2P / TURN | `N2NS` 自定义帧（SYN/DATA/ACK/FIN + 重传 + 窗口） | UDP |
| WS | JSON（`app_stream_open` / `app_stream_data` + base64） | WebSocket |

---

## 🚀 快速开始

### 前提

1. Android 手机装了 [Termux](https://f-droid.org/packages/com.termux/)（**强烈建议从 F-Droid 装**，Play Store 版本已停更）
2. 已部署 [edge-signal](https://github.com/goyo123321/edge-signal)，拿到 `wss://xxx.workers.dev` 地址

### 一键安装

```bash
curl -fsSL -o ~/install-n2n.sh https://raw.githubusercontent.com/goyo123321/n2n-termux/main/install.sh
bash ~/install-n2n.sh
```

回车后按提示输入：

```
Worker WSS 地址: wss://url.jyece.kdns.fr
连接密码 CONNECT_TOKEN:     ← 无则直接回车
房间名 ROOM_ID: testroom
客户端 ID CLIENT_ID: termux-a
显示名 NODE_NAME: Termux-A
SOCKS5 监听地址 (127.0.0.1:1080):     ← 直接回车
```

装完自动启动。

### 后台运行

```bash
nohup ~/.n2n-termux/start.sh > ~/n2n.log 2>&1 &

# 看日志
tail -f ~/n2n.log
```

### 停止

```bash
~/.n2n-termux/stop.sh
```

---

## 📖 使用

### 应用通过 SOCKS5 访问虚拟 IP

**单次命令**（curl 支持 `--socks5`）：

```bash
curl --socks5 127.0.0.1:1080 http://100.64.0.3:8080/
```

**全局代理**（环境变量）：

```bash
export ALL_PROXY=socks5://127.0.0.1:1080
curl http://100.64.0.3:8080/
```

**SSH 通过 SOCKS5**：

```bash
ssh -o ProxyCommand='nc -X 5 -x 127.0.0.1:1080 %h %p' user@100.64.0.3
```

**其他 SOCKS5 客户端**：

| 应用 | 配置方式 |
|------|---------|
| `git` | `git config --global http.proxy socks5://127.0.0.1:1080` |
| `wget` | 不支持 SOCKS5，需 `proxychains` |
| `curl` | `--socks5 127.0.0.1:1080` |
| `scp` | `scp -o ProxyCommand='nc -X 5 -x 127.0.0.1:1080 %h %p'` |

### 从其他设备访问 Termux 上的服务

在 Termux 里起服务（例如 `python3 -m http.server 8080`），其他设备通过它们的虚拟 IP 直接访问：

```bash
# 从 PC 的 n2n-go-client
curl http://100.64.0.2:8080/
```

---

## ⚙️ 环境变量

| 变量 | 必填 | 默认值 | 说明 |
|------|:----:|--------|------|
| `SIGNALING_URL` | ✅ | — | edge-signal 的 WSS 地址 |
| `CONNECT_TOKEN` | ❌ | `""` | 连接密码（服务端配了才需要） |
| `ROOM_ID` | ❌ | `default-room` | 房间名，只允许 `[A-Za-z0-9_-]+` |
| `CLIENT_ID` | ❌ | 自动生成并持久化 | 客户端唯一 ID，多设备必须不同 |
| `NODE_NAME` | ❌ | hostname | 面板显示名 |
| `SOCKS5_LISTEN` | ❌ | `127.0.0.1:1080` | SOCKS5 监听地址 |
| `STUN_SERVERS` | ❌ | 国内 STUN | STUN 服务器列表（逗号分隔） |
| `UDP_PORT` | ❌ | `0` | P2P UDP 端口，`0`=自动分配 |

**虚拟网段**由服务端 `VIRTUAL_NETWORK` 决定，客户端从 `ready` 消息自动接收。

---

## 📋 启动日志示例

正常启动：

```
[DNS] 使用 /data/data/com.termux/files/usr/etc/resolv.conf 的 2 个 nameserver: [8.8.8.8:53 8.8.4.4:53]
n2n-termux v1.0.5 启动
[配置] 未设置 CONNECT_TOKEN
[P2P] UDP 监听端口 37221
[NAT] STUN stun.chat.bilibili.com:3478 → 120.239.134.121:47877
[NAT] STUN stun.hitv.com:3478 → 120.239.134.121:47878
[NAT] HardNAT（对称），ports_diff=1 pub=120.239.134.121:47877 endpoints=2
[WS] 连接 wss://url.jyece.kdns.fr/ws/testroom?cid=termux-a
已连接信令，Client ID: termux-a，节点名: Termux-A
分配虚拟 IP: 100.64.0.2（网段 100.64.0.0/16）
[信令] 服务端看到的本机出口 IP: 47.76.28.111（WS/TCP 出口，仅参考）
[信令] 上报 p2p_metadata: natType=HardNAT publicEndpoint="120.239.134.121:47877" multiExit=true assisted=2
[SOCKS5] 监听 127.0.0.1:1080（用 curl --socks5 或 ALL_PROXY 访问虚拟 IP）
[TURN] endpoint: 111.171.194.230:3478 (scheme=turn transport=udp)
[TURN-Lite] ✅ Allocation 成功: relay=111.171.194.230:63458
[Keepalive] 启动，每 5s 刷新 3 个 STUN 服务器（仅在有 peer 时）

================= 本机信息 =================
  Client ID   : termux-a
  节点名       : Termux-A
  虚拟 IP     : 100.64.0.2
  虚拟网段    : 100.64.0.0/16
  P2P 端口    : 37221
  模式         : proxy (SOCKS5)
==========================================
```

P2P 打洞成功：

```
[NAT-HOLE] 开始打洞 role=1 target=120.230.243.39:5417 rung=0 mode=3 ttl=7
[NAT-HOLE] 模式：不同 STUN IP，全端口扫描
[NAT-HOLE] ✅ 成功 (assisted) role=1 target=120.230.243.39 attempts=3
[连接] termux-b → P2P 直连（prev=）
```

**P2P 数据面成功**（真 P2P）：

```
[SOCKS5] CONNECT 100.64.0.3:8080
[SOCKS5] CONNECT 100.64.0.3:8080 → P2P 直连 (stream=2)
```

**P2P 失败回落 WS**：

```
[SOCKS5] CONNECT 100.64.0.3:8080
[P2P-STREAM] 2 建流超时（2s），保留 conn 让调用方回落
[SOCKS5] P2P 建流失败(syn timeout)，回落 WS 中继
[SOCKS5] CONNECT 100.64.0.3:8080 → WS 中继 (stream=termux-a-1)
```

---

## 🔥 三级降级

```
① P2P UDP 直连（首选）
   ├─ 延迟 10-50ms
   ├─ 不消耗任何资源
   └─ 打洞失败则降级
       │
       ▼
② TURN 中继
   ├─ 延迟 30-100ms
   ├─ 消耗 TURN 服务器带宽（Cloudflare 1000GB/月免费）
   └─ TURN 不可用则降级
       │
       ▼
③ WebSocket 中继
   ├─ 延迟 100-300ms
   ├─ 消耗 Worker 请求配额（每天 10 万）
   └─ 最后兜底
```

**每对 peer 独立决策**：A↔B 可以走 P2P，A↔C 走 TURN，A↔D 走 WS。

---

## 🔍 P2P 数据流原理

**发起方（A）**：

```
curl 通过 SOCKS5 发起 → "连 100.64.0.3:8080"
   ↓
socks5.go 查 peer 表 → 找到 termux-b
   ↓
relayMgr.IsP2P("termux-b") == true → 走 P2P
   ↓
p2p_stream.go: OpenP2PStream()
   ↓
构造 N2NS SYN 帧（含 targetPort=8080）
   ↓
udpConn.WriteToUDP() 直发 B 的公网地址
   ↓
等 SYNACK（超时 2s）
```

**接收方（B）**：

```
udpReadLoop 收到 N2NS 帧
   ↓
findPeerIDByAddr → 匹配 termux-a
   ↓
acceptSYN → dial 127.0.0.1:8080
   ↓
回 N2NS SYNACK (Ack=1)
   ↓
启动数据泵：读 localConn → N2NS DATA → 发回 A
```

**数据帧格式**（21 字节头 + payload）：

```
偏移  长度  字段
────────────────────────────
0     4     "N2NS"      魔数
4     1     ver         版本 1
5     1     type        SYN=1 SYNACK=2 DATA=3 ACK=4 FIN=5 RST=6
6     4     streamID    大端 uint32
10    4     seq         本帧序列号
14    4     ack         确认收到的对方 seq
18    1     flags       保留
19    2     payloadLen  大端 uint16
21    N     payload     数据
```

**可靠性机制**：

- 每帧带序列号 + ACK，接收方按序交付
- 未 ACK 200ms 重传，最多 8 次
- 滑动窗口 16 帧
- 乱序缓存后按序交付

**关键代码路径**：`p2p_stream.go` 的 `sendP2PFrame` 里 `viaTURN=false` 分支：

```go
// 不走 TURN —— 真 P2P 直发
_, err := e.udpConn.WriteToUDP(buf, addr)
```

---

## 🐛 DNS 修复

Termux 环境有个坑：

- `/etc` 是 `/system/etc` 的符号链接，Android 只读，**里面没有 `resolv.conf`**
- Termux 真正的 DNS 配置在 `$PREFIX/etc/resolv.conf`
- **Go 静态二进制（`CGO_ENABLED=0`）不经过 libc**，`termux-exec` 拦不到它
- 结果：Go 硬编码读 `/etc/resolv.conf` 失败 → fallback 到 `[::1]:53` → `connection refused`

**客户端内置修复**：启动时主动读 `$PREFIX/etc/resolv.conf`，覆盖 `net.DefaultResolver`。

**验证**：启动日志第一行应该有：

```
[DNS] 使用 /data/data/com.termux/files/usr/etc/resolv.conf 的 2 个 nameserver: [8.8.8.8:53 8.8.4.4:53]
```

**自定义 DNS**：改 `$PREFIX/etc/resolv.conf` 即可。

---

## ❓ 常见问题

### Q: 装完启动报 `请设置 SIGNALING_URL`？

正常——`install.sh` 装的是二进制，不负责启动。用 `~/.n2n-termux/start.sh` 启动（它会 source `config.env`）。

### Q: SOCKS5 起来了吗？

```bash
ss -tlnp 2>/dev/null | grep 1080
# 期望：LISTEN 127.0.0.1:1080
```

或直接测协议握手：

```bash
printf '\x05\x01\x00' | nc 127.0.0.1 1080 | xxd
# 期望：00000000: 0500
```

### Q: curl 返回 `000` 立即失败？

**错误**：`找不到 VIP xxx 对应的 peer`——说明对端不在线或已离线。

**检查**：

```bash
grep "节点上线\|节点离开" ~/n2n.log | tail -5
```

### Q: curl 慢（3-5 秒）？

**原因**：每次请求都先尝试 P2P，2 秒超时后才回落 WS。

**排查**：

1. 看 A 端日志有没有 `[P2P-STREAM] 建流超时`
2. 看 B 端日志有没有 `[P2P-STREAM] 收到 SYN`

如果 A 建流超时但 B 没收到 SYN——**说明 UDP 单向不通**（CGNAT 特性）。

**优化**：等 P2P 数据面修复后，TTFB 会掉到 50ms 左右。

### Q: P2P 打洞总失败？

**检查两个设备的 UDP 出口 IP 是否相同**：

```
A: UDP 120.239.134.121:47877
B: UDP 120.230.243.39:5417     ← 应不同
```

**相同 = 同 CGNAT 出口（hairpin 场景），P2P 大概率失败**。

**换不同网络**：一台用 4G，一台用 WiFi，出口 IP 就会不同。

**即使 P2P 失败，TURN 或 WS 依然能通**——只是延迟高一些。

### Q: TURN 一直失败/刷屏？

**原因**：服务端配的 TURN 服务器访问不通。

**修复**：

**方案 1（推荐）**：服务端 `edge-signal/wrangler.toml` 里注释掉 `TURN_SERVERS`：

```toml
# TURN_SERVERS = "turn://..."
```

**方案 2**：客户端连续失败 5 次后自动放弃（v1.0.2 起内置）。

### Q: Termux 切后台被杀？

**解决**：

```bash
termux-wake-lock
nohup ~/.n2n-termux/start.sh > ~/n2n.log 2>&1 &
```

**需要装 [Termux:API](https://f-droid.org/packages/com.termux.api/)**。

**长期方案**：Android 设置 → 电池 → 电池优化 → Termux → 不优化。

### Q: 如何让 Termux 开机自启？

装 [Termux:Boot](https://f-droid.org/packages/com.termux.boot/)：

```bash
mkdir -p ~/.termux/boot
cat > ~/.termux/boot/n2n.sh <<'EOF'
#!/data/data/com.termux/files/usr/bin/sh
termux-wake-lock
nohup $HOME/.n2n-termux/start.sh > $HOME/n2n.log 2>&1 &
EOF
chmod +x ~/.termux/boot/n2n.sh
```

### Q: 支持域名吗？

**不支持**。SOCKS5 请求带域名会返回 `Address type not supported`。

**原因**：n2n 虚拟网里没有 DNS 服务。

**变通**：直接用虚拟 IP，或在本地 hosts 里加映射。

### Q: 支持 IPv6 / UDP 吗？

**不支持**。SOCKS5 请求解析不支持 IPv6 目标；`UDP ASSOCIATE` 命令未实现（只支持 `CONNECT`）。

**原因**：底层数据面是可靠流（序列号 + ACK + 重传），对 UDP 应用不友好。绝大多数 Termux 场景（SSH/HTTP/DB）只需要 TCP。

### Q: 一次能跑几个实例？

同一台手机可以跑多个（不同 `SOCKS5_LISTEN` 端口、不同 `CLIENT_ID`、不同 `UDP_PORT`）。

**同机多实例测不出真 P2P**——两个实例出口 IP 相同，会走 hairpin 逻辑（大概率失败 → TURN/WS）。

**要验证 P2P 必须两台物理设备**，且出口 UDP IP 不同。

---

## 🔗 相关项目

| 项目 | 平台 | 模式 |
|------|------|------|
| [edge-signal](https://github.com/goyo123321/edge-signal) | Cloudflare Workers | 信令服务器 + 打洞协调 + WS 中继 |
| [n2n-go-client](https://github.com/goyo123321/n2n-go-client) | Linux / macOS / Windows | TUN 网卡（需 root/管理员） |
| [n2n-android](https://github.com/goyo123321/n2n-android) | Android APK | VpnService TUN |
| **n2n-termux** | Android Termux | **SOCKS5 代理（无 root）** |

三端**互通**：n2n-termux ↔ n2n-go-client ↔ n2n-android 可以互相访问。

---

## 🚨 限制

| 项 | 说明 |
|----|------|
| **应用透明性** | 需要显式配置 SOCKS5，不像 TUN 模式那样"自动生效" |
| **域名** | 不支持，只能用虚拟 IP |
| **IPv6** | 不支持 |
| **UDP 应用** | SOCKS5 只代理 TCP，`UDP ASSOCIATE` 未实现 |
| **性能** | 比 TUN 模式略慢（多一层 SOCKS5 和应用流封装） |
| **单设备 P2P** | 同机多实例测不出真 P2P（CGNAT 特性） |
| **CGNAT hairpin** | 运营商 CGNAT 通常不支持，同出口 IP 时 P2P 会失败 |

---

## 📄 License

MIT

---

## 🙏 致谢

- [pion/stun](https://github.com/pion/stun) — STUN 协议实现（NAT 探测）
- [gorilla/websocket](https://github.com/gorilla/websocket) — WebSocket 传输
- 服务端 [edge-signal](https://github.com/goyo123321/edge-signal) 提供信令、打洞协调、TURN 凭证
