#!/data/data/com.termux/files/usr/bin/bash
#
# n2n-termux 一键部署脚本
# 适用：Android Termux（无 root，SOCKS5 模式）
#
# 用法：
#   curl -fsSL https://raw.githubusercontent.com/<你>/n2n-termux/main/install.sh | bash
#
# 非交互式（自动跳过所有提问）：
#   SIGNALING_URL="wss://xxx" ROOM_ID="myroom" CLIENT_ID="termux-a" \
#     AUTO_START=1 bash install.sh
#
set -e

# ============================================================
# 颜色
# ============================================================
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

log()  { echo -e "${BLUE}[*]${NC} $1"; }
ok()   { echo -e "${GREEN}[✓]${NC} $1"; }
warn() { echo -e "${YELLOW}[!]${NC} $1"; }
err()  { echo -e "${RED}[✗]${NC} $1" >&2; }

# ============================================================
# 配置
# ============================================================
REPO="${REPO:-goyo123321/n2n-termux}"     # ★ 改成你的 GitHub 用户名
VERSION="${VERSION:-latest}"              # latest 或 v1.0.0

INSTALL_DIR="${INSTALL_DIR:-$HOME/.n2n-termux}"
BIN_DIR="$INSTALL_DIR/bin"
BIN="$BIN_DIR/n2n-client"
CONFIG_FILE="$INSTALL_DIR/config.env"

SIGNALING_URL="${SIGNALING_URL:-}"
CONNECT_TOKEN="${CONNECT_TOKEN:-}"
ROOM_ID="${ROOM_ID:-}"
CLIENT_ID="${CLIENT_ID:-}"
NODE_NAME="${NODE_NAME:-}"
SOCKS5_LISTEN="${SOCKS5_LISTEN:-127.0.0.1:1080}"
AUTO_START="${AUTO_START:-0}"

# ============================================================
# 环境检查
# ============================================================
check_env() {
    if [ ! -d "/data/data/com.termux" ]; then
        warn "看起来不是 Termux 环境（/data/data/com.termux 不存在）"
        warn "脚本仍会继续，但路径假设是 Termux 的"
        echo ""
    fi

    for cmd in curl uname; do
        if ! command -v "$cmd" >/dev/null 2>&1; then
            err "缺少依赖: $cmd"
            echo "  安装: pkg install $cmd"
            exit 1
        fi
    done
}

# ============================================================
# 架构检测
# ============================================================
detect_arch() {
    local arch
    arch="$(uname -m)"

    case "$arch" in
        aarch64|arm64)
            GOARCH="arm64"
            ;;
        armv7l|armv7)
            GOARCH="armv7"
            ;;
        x86_64|amd64)
            GOARCH="amd64"
            ;;
        armv8l)
            # 32 位系统跑在 64 位 CPU 上
            GOARCH="armv7"
            ;;
        i686|i386)
            err "不支持的架构: $arch（32 位 x86 无 Go 官方支持）"
            exit 1
            ;;
        *)
            err "不支持的架构: $arch"
            exit 1
            ;;
    esac

    log "检测到架构: $arch → linux/$GOARCH"
}

# ============================================================
# 交互式配置
# ============================================================
ask_config() {
    echo ""
    echo "==================== 配置 ===================="
    echo "（直接回车 = 使用括号里的默认值）"
    echo ""

    # WSS 地址
    while [ -z "$SIGNALING_URL" ]; do
        echo -e "${YELLOW}Worker WSS 地址（必填）${NC}"
        echo "  示例: wss://edge-signal.xxx.workers.dev"
        read -rp "  WSS 地址: " SIGNALING_URL
        SIGNALING_URL="$(echo "$SIGNALING_URL" | tr -d '[:space:]')"

        if [ -z "$SIGNALING_URL" ]; then
            err "不能为空"
            echo ""
            continue
        fi
        if [[ ! "$SIGNALING_URL" =~ ^wss?:// ]]; then
            err "格式错误：必须以 wss:// 或 ws:// 开头"
            SIGNALING_URL=""
            echo ""
            continue
        fi
    done
    ok "WSS: $SIGNALING_URL"
    echo ""

    # CONNECT_TOKEN
    if [ -z "$CONNECT_TOKEN" ]; then
        read -rp "连接密码 CONNECT_TOKEN (无则回车): " CONNECT_TOKEN
    fi

    # ROOM_ID
    while [ -z "$ROOM_ID" ]; do
        read -rp "房间名 ROOM_ID (default-room): " input
        input="${input:-default-room}"
        if [[ ! "$input" =~ ^[A-Za-z0-9_-]+$ ]]; then
            err "只允许字母/数字/下划线/短横线"
            continue
        fi
        ROOM_ID="$input"
    done

    # CLIENT_ID
    if [ -z "$CLIENT_ID" ]; then
        local default_id
        default_id="$(hostname)"
        read -rp "客户端 ID CLIENT_ID ($default_id): " input
        CLIENT_ID="${input:-$default_id}"
    fi

    # NODE_NAME
    if [ -z "$NODE_NAME" ]; then
        read -rp "显示名 NODE_NAME ($(hostname)): " input
        NODE_NAME="${input:-$(hostname)}"
    fi

    # SOCKS5 监听地址
    read -rp "SOCKS5 监听地址 ($SOCKS5_LISTEN): " input
    SOCKS5_LISTEN="${input:-$SOCKS5_LISTEN}"

    echo "==============================================="
    echo ""
}

# ============================================================
# 保存配置
# ============================================================
save_config() {
    mkdir -p "$INSTALL_DIR" "$BIN_DIR"

    cat > "$CONFIG_FILE" <<EOF
# n2n-termux 配置
# 由 install.sh 生成于 $(date)

SIGNALING_URL="$SIGNALING_URL"
CONNECT_TOKEN="$CONNECT_TOKEN"
ROOM_ID="$ROOM_ID"
CLIENT_ID="$CLIENT_ID"
NODE_NAME="$NODE_NAME"
SOCKS5_LISTEN="$SOCKS5_LISTEN"
EOF

    chmod 600 "$CONFIG_FILE"
    ok "配置已保存: $CONFIG_FILE"
}

# ============================================================
# 获取版本号
# ============================================================
resolve_version() {
    if [ "$VERSION" != "latest" ]; then
        echo "$VERSION"
        return
    fi

    log "查询最新版本..."
    local tag
    tag=$(curl -sSL "https://api.github.com/repos/$REPO/releases/latest" \
        | grep '"tag_name"' | head -1 | sed 's/.*: "\(.*\)".*/\1/')

    if [ -z "$tag" ]; then
        err "无法获取最新版本号"
        err "手动指定：VERSION=v1.0.0 bash install.sh"
        exit 1
    fi

    echo "$tag"
}

# ============================================================
# 下载二进制
# ============================================================
download_binary() {
    local tag="$1"
    local file="n2n-client-linux-$GOARCH"
    local url="https://github.com/$REPO/releases/download/$tag/$file"
    local sha_url="$url.sha256"

    # 已有同名二进制且大小 > 1MB → 跳过
    if [ -f "$BIN" ]; then
        local size
        size=$(stat -c%s "$BIN" 2>/dev/null || echo 0)
        if [ "$size" -gt 1000000 ]; then
            ok "二进制已存在: $BIN（跳过下载）"
            return
        fi
    fi

    mkdir -p "$BIN_DIR"

    log "下载 $tag / $file"
    log "  URL: $url"

    if ! curl -L --fail --progress-bar -o "$BIN" "$url"; then
        err "下载失败"
        echo ""
        echo "可能原因："
        echo "  1. 版本号不存在: $tag"
        echo "  2. Release 未发布（工作流里勾了 publish？）"
        echo "  3. GitHub 网络问题"
        echo ""
        echo "手动检查："
        echo "  https://github.com/$REPO/releases"
        exit 1
    fi

    chmod +x "$BIN"

    # 校验 sha256（可选，失败不阻断）
    local expected actual
    if expected=$(curl -sSL --fail "$sha_url" 2>/dev/null | awk '{print $1}'); then
        actual=$(sha256sum "$BIN" | awk '{print $1}')
        if [ "$expected" = "$actual" ]; then
            ok "sha256 校验通过"
        else
            warn "sha256 不匹配！"
            echo "  期望: $expected"
            echo "  实际: $actual"
            warn "文件可能损坏或被篡改，建议手动确认"
        fi
    else
        log "跳过 sha256 校验（.sha256 文件不存在）"
    fi

    local size
    size=$(du -h "$BIN" | cut -f1)
    ok "下载完成: $BIN ($size)"
}

# ============================================================
# 生成启动脚本
# ============================================================
create_launcher() {
    local launcher="$INSTALL_DIR/start.sh"

    cat > "$launcher" <<'EOF'
#!/data/data/com.termux/files/usr/bin/bash
# n2n-termux 启动脚本（由 install.sh 生成）
set -e

INSTALL_DIR="${INSTALL_DIR:-$HOME/.n2n-termux}"
CONFIG_FILE="$INSTALL_DIR/config.env"
BIN="$INSTALL_DIR/bin/n2n-client"

if [ ! -f "$CONFIG_FILE" ]; then
    echo "配置文件不存在: $CONFIG_FILE"
    echo "请先运行 install.sh"
    exit 1
fi

if [ ! -x "$BIN" ]; then
    echo "二进制不存在: $BIN"
    echo "请先运行 install.sh"
    exit 1
fi

set -a
# shellcheck disable=SC1090
source "$CONFIG_FILE"
set +a

exec "$BIN"
EOF

    chmod +x "$launcher"
    ok "启动脚本: $launcher"
}

# ============================================================
# 主流程
# ============================================================
main() {
    echo ""
    echo "========================================="
    echo "   n2n-termux 一键部署"
    echo "========================================="
    echo ""

    check_env
    detect_arch

    # 配置：已有 → 询问是否重配；没有 → 交互输入
    if [ -f "$CONFIG_FILE" ] && [ -z "$SIGNALING_URL" ]; then
        warn "已存在配置: $CONFIG_FILE"
        read -rp "重新配置？(y/N): " input
        if [[ ! "$input" =~ ^[Yy]$ ]]; then
            log "使用已有配置"
            set -a
            # shellcheck disable=SC1090
            source "$CONFIG_FILE"
            set +a
        else
            ask_config
            save_config
        fi
    else
        ask_config
        save_config
    fi

    # 下载
    local tag
    tag=$(resolve_version)
    log "使用版本: $tag"
    download_binary "$tag"

    # 启动脚本
    create_launcher

    echo ""
    echo "========================================="
    ok "部署完成！"
    echo "========================================="
    echo ""
    echo "  二进制:  $BIN"
    echo "  配置:    $CONFIG_FILE"
    echo ""
    echo "  前台启动:"
    echo -e "    ${GREEN}$INSTALL_DIR/start.sh${NC}"
    echo ""
    echo "  后台启动:"
    echo -e "    ${GREEN}nohup $INSTALL_DIR/start.sh > ~/n2n.log 2>&1 &${NC}"
    echo ""
    echo "  停止:"
    echo -e "    ${GREEN}pkill -f n2n-client${NC}"
    echo ""
    echo "  用 SOCKS5:"
    echo "    curl --socks5 127.0.0.1:1080 http://<对端虚拟IP>:port/"
    echo "    export ALL_PROXY=socks5://127.0.0.1:1080"
    echo ""

    # 自动启动判断
    if [ "$AUTO_START" = "1" ]; then
        log "AUTO_START=1，直接启动"
        exec "$INSTALL_DIR/start.sh"
    fi

    read -rp "现在启动？(Y/n): " input
    if [[ "$input" =~ ^[Nn]$ ]]; then
        log "已跳过启动"
        echo ""
        echo "  手动启动: $INSTALL_DIR/start.sh"
        echo ""
        exit 0
    fi

    exec "$INSTALL_DIR/start.sh"
}

main "$@"
