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
- **自动 TLS 证书** — 内置 Termux CA 证书探测，彻底解决 `x509: unknown authority` 报错
- **四级降级** — `LAN → P2P → TURN → WS`，连接永远可用
- **轻量无依赖** — 纯 Go 标准库，无需 gVisor，编译仅需 2 秒，二进制 ~10MB
- **UDP 保活** — 每 5 秒刷新 STUN 映射，防止 CGNAT 端口漂移
- **P2P 通道保活** — 每 15 秒往对端发 N2NS PING 多目标探针
- **零配置网段** — 服务端通过 `VIRTUAL_NETWORK` 下发，客户端自动适配

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

### 推荐：手动下载运行（最稳定）

Termux 的网络环境经常导致一键脚本卡住，推荐直接手动下载：

```bash
# 1. 下载最新二进制（以 ARM64 为例）
curl -L -o ~/n2n-client https://github.com/goyo123321/n2n-termux/releases/latest/download/n2n-client-linux-arm64

# 2. 给执行权限
chmod +x ~/n2n-client

# 3. 运行
STUN_SERVERS="stun.chat.bilibili.com:3478,stun.hitv.com:3478" \
SIGNALING_URL="wss://url.your-domain.com" \
ROOM_ID="default-room" \
CLIENT_ID="termux-a" \
NODE_NAME="My-Termux" \
SOCKS5_LISTEN="127.0.0.1:1080" \
~/n2n-client
```

### 可选：一键安装脚本

```bash
SIGNALING_URL="wss://url.your-domain.com" \
ROOM_ID="default-room" \
CLIENT_ID="termux-a" \
AUTO_START=1 \
bash <(curl -fsSL https://raw.githubusercontent.com/goyo123321/n2n-termux/main/install.sh)
```

### 后台运行（推荐）

```bash
nohup env \
  SIGNALING_URL="wss://url.your-domain.com" \
  ROOM_ID="default-room" \
  CLIENT_ID="termux-a" \
  NODE_NAME="My-Termux" \
  ~/n2n-client > ~/n2n.log 2>&1 &
  
# 查看日志
tail -f ~/n2n.log
```

---

## ⚙️ 环境变量配置

| 变量 | 必填 | 默认值 | 说明 |
|:---|:---|:---|:---|
| `SIGNALING_URL` | ✅ | — | edge-signal 的 WSS 地址 |
| `ROOM_ID` | ❌ | `default-room` | 房间名 |
| `CLIENT_ID` | ❌ | 自动生成并持久化 | 客户端唯一 ID |
| `NODE_NAME` | ❌ | hostname | 显示名 |
| `SOCKS5_LISTEN` | ❌ | `127.0.0.1:1080` | SOCKS5 监听地址 |
| `CONNECT_TOKEN` | ❌ | `""` | 连接密码（Worker 配了才需要）|
| `STUN_SERVERS` | ❌ | 内置 | 自定义 STUN 服务器 |

---

## ❓ 常见问题

### Q: 启动报错 `tls: failed to verify certificate: x509: certificate signed by unknown authority`？
**原因**：Termux 没有系统 CA 证书路径，Go 标准库无法验证 WSS 证书。
**解决**：最新版（v1.1.18+）已内置自动探测机制，会从 `$PREFIX/etc/tls/cert.pem` 自动加载。如果仍然报错，请手动安装证书：
```bash
pkg install ca-certificates
```

### Q: 为什么 PC 能 ping 通 Termux，但 Termux 不能 ping 通 PC？
**原因**：Termux 走的是 SOCKS5 代理，而 SOCKS5 协议本身不支持 ICMP 和 UDP。
**解决**：这是无 root 权限的底层限制。如果需要 Termux 具备主动 Ping 能力，请使用拥有 root 权限的 Linux 设备运行 [n2n-go-client](https://github.com/goyo123321/n2n-go-client)。

### Q: 为什么 `ping 10.64.0.x` 会延迟 200ms 左右？
**原因**：如果打洞失败降级到 WS 中继，ICMP 包会走 Cloudflare Worker 转发，延迟会升高。但数据不会丢失。
**解决**：正常现象，TUN 打洞成功后会自动降回 LAN/P2P，延迟降至 10ms 以内。

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
