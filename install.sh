#!/data/data/com.termux/files/usr/bin/bash
#
# n2n-termux 一键部署 + 自动升级
#
# 用法：
#   bash install.sh
#   bash <(curl -fsSL https://raw.githubusercontent.com/goyo123321/n2n-termux/main/install.sh)
#
# 环境变量（可选）：
#   SIGNALING_URL, CONNECT_TOKEN, ROOM_ID, CLIENT_ID, NODE_NAME, SOCKS5_LISTEN
#   VERSION, INSTALL_DIR, FORCE_UPDATE, NO_LAUNCH, AUTO_START, CHECK_ONLY
#

set -euo pipefail

# ============================================================
# 变量初始化（set -u 修复：引用未定义变量会报错）
# ============================================================
REPO="goyo123321/n2n-termux"
BIN_NAME="n2n-client"

SIGNALING_URL="${SIGNALING_URL:-}"
CONNECT_TOKEN="${CONNECT_TOKEN:-}"
ROOM_ID="${ROOM_ID:-}"
CLIENT_ID="${CLIENT_ID:-}"
NODE_NAME="${NODE_NAME:-}"
SOCKS5_LISTEN="${SOCKS5_LISTEN:-127.0.0.1:1080}"
VERSION="${VERSION:-}"
FORCE_UPDATE="${FORCE_UPDATE:-0}"
NO_LAUNCH="${NO_LAUNCH:-0}"
AUTO_START="${AUTO_START:-0}"
CHECK_ONLY="${CHECK_ONLY:-0}"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.n2n-termux}"

BIN_DIR="$INSTALL_DIR/bin"
BIN_PATH="$BIN_DIR/$BIN_NAME"
VERSION_FILE="$INSTALL_DIR/.version"
CONFIG_FILE="$INSTALL_DIR/config.env"
LAUNCHER="$INSTALL_DIR/start.sh"

GITHUB_API="https://api.github.com/repos/${REPO}"
FALLBACK_VERSION="v1.1.18"

# ============================================================
# curl | bash 场景下把 stdin 重定向到 tty
# ============================================================
if [ ! -t 0 ] && [ -r /dev/tty ]; then
    exec < /dev/tty || true
fi

# ============================================================
# 颜色
# ============================================================
RED='\033[0;31m'; GREEN='\033[0;32m'
YELLOW='\033[1;33m'; BLUE='\033[0;34m'; CYAN='\033[0;36m'; NC='\033[0m'
log()  { printf "${BLUE}[*]${NC} %s\n" "$*"; }
ok()   { printf "${GREEN}[✓]${NC} %s\n" "$*"; }
warn() { printf "${YELLOW}[!]${NC} %s\n" "$*"; }
err()  { printf "${RED}[✗]${NC} %s\n" "$*" >&2; }
info() { printf "${CYAN}  %s${NC}\n" "$*"; }

# ============================================================
# 环境检查
# ============================================================
check_env() {
    if [ ! -d "/data/data/com.termux" ]; then
        warn "看起来不是 Termux 环境"
    fi

    if ! command -v curl >/dev/null 2>&1 && ! command -v wget >/dev/null 2>&1; then
        err "缺少 curl 或 wget"
        info "安装: pkg install -y curl wget"
        exit 1
    fi

    # Termux CA 证书检查
    if [ ! -f "$PREFIX/etc/tls/cert.pem" ] && [ ! -f "$PREFIX/etc/ca-certificates/ca-bundle.crt" ]; then
        warn "未找到 CA 证书，尝试安装 ca-certificates..."
        if command -v pkg >/dev/null 2>&1; then
            pkg install -y ca-certificates || warn "自动安装失败，请手动: pkg install ca-certificates"
        fi
    fi
}

# ============================================================
# 架构检测
# ============================================================
GOARCH=""
detect_arch() {
    local arch
    arch="$(uname -m)"

    case "$arch" in
        aarch64|arm64)       GOARCH="arm64" ;;
        armv7l|armv7|armv8l) GOARCH="armv7" ;;
        x86_64|amd64)        GOARCH="amd64" ;;
        *) err "不支持的架构: $arch"; exit 1 ;;
    esac

    log "架构: $arch → linux/$GOARCH"
}

# ============================================================
# 版本管理
# ============================================================
read_installed_version() {
    if [ -f "$VERSION_FILE" ]; then
        cat "$VERSION_FILE" 2>/dev/null | tr -d '[:space:]'
    else
        echo ""
    fi
}

write_installed_version() {
    mkdir -p "$INSTALL_DIR"
    echo "$1" > "$VERSION_FILE"
}

resolve_latest_version() {
    if [ -n "$VERSION" ]; then
        echo "$VERSION"
        return 0
    fi

    local resp code body tag
    if command -v curl >/dev/null 2>&1; then
        resp=$(curl -sSL -w "\n__HTTP_CODE__:%{http_code}" \
            "${GITHUB_API}/releases/latest" 2>&1) || {
            warn "无法访问 GitHub API，使用内置版本: $FALLBACK_VERSION"
            echo "$FALLBACK_VERSION"
            return 0
        }
    else
        warn "无 curl，使用内置版本: $FALLBACK_VERSION"
        echo "$FALLBACK_VERSION"
        return 0
    fi

    code=$(echo "$resp" | grep -oE '__HTTP_CODE__:[0-9]+' | cut -d: -f2 | tail -1)
    body=$(echo "$resp" | sed 's/__HTTP_CODE__:[0-9]*$//')

    case "$code" in
        200)
            tag=$(echo "$body" | grep -m1 '"tag_name"' | sed 's/.*: "\(.*\)".*/\1/')
            if [ -z "$tag" ]; then
                warn "解析 tag_name 失败，使用内置版本"
                echo "$FALLBACK_VERSION"
            else
                echo "$tag"
            fi
            ;;
        404)
            warn "仓库无 Release，使用内置版本: $FALLBACK_VERSION"
            echo "$FALLBACK_VERSION"
            ;;
        *)
            warn "GitHub API 失败（HTTP $code），使用内置版本: $FALLBACK_VERSION"
            echo "$FALLBACK_VERSION"
            ;;
    esac
}

# ============================================================
# 停止运行中的客户端
# ============================================================
stop_running_client() {
    if ! command -v pgrep >/dev/null 2>&1; then
        return 0
    fi

    local pids
    pids=$(pgrep -f "${BIN_PATH}" 2>/dev/null || true)
    if [ -z "$pids" ]; then
        return 0
    fi

    log "检测到运行中的 n2n-client (PID: $pids)，停止..."
    # shellcheck disable=SC2086
    kill $pids 2>/dev/null || true
    sleep 1

    pids=$(pgrep -f "${BIN_PATH}" 2>/dev/null || true)
    if [ -n "$pids" ]; then
        warn "进程未响应 SIGTERM，使用 SIGKILL"
        # shellcheck disable=SC2086
        kill -9 $pids 2>/dev/null || true
        sleep 0.5
    fi
    ok "旧进程已停止"
}

# ============================================================
# 下载二进制
# ============================================================
download_binary() {
    local tag="$1"
    local file="n2n-client-linux-$GOARCH"
    local url="https://github.com/$REPO/releases/download/$tag/$file"

    mkdir -p "$BIN_DIR"
    log "下载: $tag / $file"

    local ok_dl=0
    if command -v wget >/dev/null 2>&1; then
        if wget -O "$BIN_PATH" "$url"; then
            ok_dl=1
        fi
    fi
    if [ "$ok_dl" = "0" ] && command -v curl >/dev/null 2>&1; then
        if curl -L --fail -o "$BIN_PATH" "$url"; then
            ok_dl=1
        fi
    fi

    if [ "$ok_dl" = "0" ]; then
        err "下载失败: $url"
        echo ""
        echo "可能原因："
        echo "  1. 版本号不存在: $tag"
        echo "  2. Release 未上传 $file"
        echo "  3. GitHub 网络问题"
        echo ""
        echo "手动检查: https://github.com/$REPO/releases/tag/$tag"
        exit 1
    fi

    # ELF 校验
    local magic
    magic=$(head -c 4 "$BIN_PATH" | od -An -tx1 | tr -d ' \n')
    if [ "$magic" != "7f454c46" ]; then
        err "下载的文件不是 ELF 二进制（可能是 HTML 错误页）"
        head -c 64 "$BIN_PATH" | od -c
        rm -f "$BIN_PATH"
        exit 1
    fi

    chmod +x "$BIN_PATH"
    ok "已安装: $BIN_PATH ($(du -h "$BIN_PATH" | cut -f1))"
}

# ============================================================
# 交互式配置
# ============================================================
ask_config() {
    echo ""
    echo "==================== 配置 ===================="
    echo "（直接回车 = 使用括号里的默认值）"
    echo ""

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

    if [ -z "$CONNECT_TOKEN" ]; then
        read -rp "连接密码 CONNECT_TOKEN (无则回车): " CONNECT_TOKEN
    fi

    while [ -z "$ROOM_ID" ]; do
        read -rp "房间名 ROOM_ID (default-room): " input
        input="${input:-default-room}"
        if [[ ! "$input" =~ ^[A-Za-z0-9_-]+$ ]]; then
            err "只允许字母/数字/下划线/短横线"
            continue
        fi
        ROOM_ID="$input"
    done

    if [ -z "$CLIENT_ID" ]; then
        local default_id
        default_id="$(hostname 2>/dev/null || echo termux)"
        read -rp "客户端 ID CLIENT_ID ($default_id): " input
        CLIENT_ID="${input:-$default_id}"
    fi

    if [ -z "$NODE_NAME" ]; then
        local default_name
        default_name="$(hostname 2>/dev/null || echo Termux)"
        read -rp "显示名 NODE_NAME ($default_name): " input
        NODE_NAME="${input:-$default_name}"
    fi

    read -rp "SOCKS5 监听地址 ($SOCKS5_LISTEN): " input
    SOCKS5_LISTEN="${input:-$SOCKS5_LISTEN}"

    echo "==============================================="
    echo ""
}

# ============================================================
# 保存配置
# ============================================================
save_config() {
    mkdir -p "$INSTALL_DIR"

    cat > "$CONFIG_FILE" <<EOF
# n2n-termux 配置
# 由 install.sh 生成于 $(date 2>/dev/null || echo now)

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
# 生成启动脚本
# ============================================================
create_launcher() {
    cat > "$LAUNCHER" <<'EOF'
#!/data/data/com.termux/files/usr/bin/bash
set -euo pipefail

INSTALL_DIR="${INSTALL_DIR:-$HOME/.n2n-termux}"
CONFIG_FILE="$INSTALL_DIR/config.env"
BIN="$INSTALL_DIR/bin/n2n-client"

if [ ! -f "$CONFIG_FILE" ]; then
    echo "配置文件不存在: $CONFIG_FILE"
    exit 1
fi
if [ ! -x "$BIN" ]; then
    echo "二进制不存在: $BIN"
    exit 1
fi

set -a
source "$CONFIG_FILE"
set +a

exec "$BIN" "$@"
EOF

    chmod +x "$LAUNCHER"
    ok "启动脚本: $LAUNCHER"
}

# ============================================================
# 主流程
# ============================================================
main() {
    echo ""
    echo "========================================="
    echo "   n2n-termux 一键部署"
    echo "   https://github.com/$REPO"
    echo "========================================="
    echo ""

    check_env
    detect_arch

    local installed latest need_update
    installed="$(read_installed_version)"

    log "查询最新版本..."
    latest="$(resolve_latest_version)"

    if [ -n "$installed" ]; then
        log "本地已装: $installed"
    else
        log "本地未装"
    fi
    log "远端最新: $latest"

    need_update=1
    if [ -n "$installed" ] && [ "$installed" = "$latest" ] && [ "$FORCE_UPDATE" != "1" ]; then
        need_update=0
    fi

    if [ "$CHECK_ONLY" = "1" ]; then
        echo ""
        if [ "$need_update" = "1" ]; then
            ok "有新版本可更新: ${installed:-无} → $latest"
            info "不带 CHECK_ONLY=1 即可升级"
        else
            ok "已是最新版本: $latest"
        fi
        exit 0
    fi

    # 配置阶段
    if [ "$AUTO_START" = "1" ]; then
        if [ -n "$SIGNALING_URL" ]; then
            log "非交互模式：使用环境变量"
            save_config
        elif [ -f "$CONFIG_FILE" ]; then
            log "非交互模式：使用已有配置 $CONFIG_FILE"
            set -a
            source "$CONFIG_FILE"
            set +a
        else
            err "AUTO_START=1 但既无环境变量也无配置文件"
            err "  至少: SIGNALING_URL=wss://... AUTO_START=1 bash install.sh"
            exit 1
        fi
    elif [ -f "$CONFIG_FILE" ] && [ -z "$SIGNALING_URL" ]; then
        warn "已存在配置: $CONFIG_FILE"
        read -rp "重新配置？(y/N): " input
        if [[ ! "$input" =~ ^[Yy]$ ]]; then
            log "使用已有配置"
            set -a
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
    if [ "$need_update" = "0" ]; then
        ok "已是最新版本: $latest（跳过下载，FORCE_UPDATE=1 可强制重装）"
    else
        if [ -n "$installed" ]; then
            log "升级中: $installed → $latest"
        else
            log "首次安装: $latest"
        fi

        stop_running_client
        download_binary "$latest"
        write_installed_version "$latest"
        ok "版本已更新: $latest"
    fi

    create_launcher

    echo ""
    echo "========================================="
    ok "部署完成！"
    echo "========================================="
    echo ""
    info "二进制:  $BIN_PATH"
    info "版本:    $latest"
    info "配置:    $CONFIG_FILE"
    echo ""
    info "前台启动: $LAUNCHER"
    info "后台启动: nohup $LAUNCHER > ~/n2n.log 2>&1 &"
    info "停止:     pkill -f n2n-client"
    echo ""

    # 启动
    if [ "$AUTO_START" = "1" ]; then
        log "AUTO_START=1，直接启动"
        exec "$LAUNCHER"
    fi

    printf "现在启动？(Y/n): "
    read -rp "" input
    if [[ "$input" =~ ^[Nn]$ ]]; then
        log "已跳过启动"
        echo ""
        info "手动启动: $LAUNCHER"
        echo ""
        exit 0
    fi

    exec "$LAUNCHER"
}

main "$@"
