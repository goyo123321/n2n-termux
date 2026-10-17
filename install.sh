#!/usr/bin/env bash
#
# n2n-termux 一键部署 + 自动升级
#
# 用法：
#   bash <(curl -fsSL https://raw.githubusercontent.com/goyo123321/n2n-termux/main/install.sh)
#
# 环境变量：
#   SIGNALING_URL     WSS 地址（必填）
#   CONNECT_TOKEN     连接密码（可选）
#   ROOM_ID           房间名，默认 default-room
#   CLIENT_ID         客户端 ID，留空自动生成并持久化
#   NODE_NAME         显示名，默认 hostname
#   SOCKS5_LISTEN     SOCKS5 监听，默认 127.0.0.1:1080
#   UDP_PORT          P2P UDP 端口，默认 0（内核分配）
#   STUN_SERVERS      自定义 STUN（可选）
#   VERSION           指定版本（如 v1.2.0）；留空自动获取最新
#   INSTALL_DIR       安装目录，默认 ~/.n2n-termux
#   FORCE_UPDATE      =1 强制重装
#   NO_LAUNCH         =1 只安装不启动
#   AUTO_START        =1 静默自动启动（后台）
#   CHECK_ONLY        =1 只检查版本不下载
#

set -euo pipefail

REPO="goyo123321/n2n-termux"
BIN_NAME="n2n-client"

SIGNALING_URL="${SIGNALING_URL:-}"
CONNECT_TOKEN="${CONNECT_TOKEN:-}"
ROOM_ID="${ROOM_ID:-}"
CLIENT_ID="${CLIENT_ID:-}"
NODE_NAME="${NODE_NAME:-}"
SOCKS5_LISTEN="${SOCKS5_LISTEN:-}"
UDP_PORT="${UDP_PORT:-0}"
STUN_SERVERS="${STUN_SERVERS:-}"
VERSION="${VERSION:-}"
FORCE_UPDATE="${FORCE_UPDATE:-0}"
NO_LAUNCH="${NO_LAUNCH:-0}"
AUTO_START="${AUTO_START:-0}"
CHECK_ONLY="${CHECK_ONLY:-0}"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.n2n-termux}"

BIN_PATH="$INSTALL_DIR/$BIN_NAME"
VERSION_FILE="$INSTALL_DIR/.version"
CONFIG_FILE="$INSTALL_DIR/config.env"
LAUNCHER="$INSTALL_DIR/start.sh"
STOPPER="$INSTALL_DIR/stop.sh"
PID_FILE="$INSTALL_DIR/n2n.pid"
LOG_FILE="$INSTALL_DIR/n2n.log"

GITHUB_API="https://api.github.com/repos/${REPO}"
GITHUB_DL="https://github.com/${REPO}/releases/download"
FALLBACK_VERSION="v1.1.22"

# 非交互式（curl | bash）时把 stdin 重定向到 tty
if [ ! -t 0 ] && [ -r /dev/tty ]; then
    exec < /dev/tty || true
fi

# 颜色
if [ -t 1 ] && [ "${TERM:-}" != "dumb" ]; then
    RED='\033[0;31m'; GREEN='\033[0;32m'
    YELLOW='\033[1;33m'; BLUE='\033[0;34m'; CYAN='\033[0;36m'; NC='\033[0m'
else
    RED=''; GREEN=''; YELLOW=''; BLUE=''; CYAN=''; NC=''
fi
log()  { printf "${BLUE}[*]${NC} %s\n" "$*"; }
ok()   { printf "${GREEN}[✓]${NC} %s\n" "$*"; }
warn() { printf "${YELLOW}[!]${NC} %s\n" "$*"; }
err()  { printf "${RED}[✗]${NC} %s\n" "$*" >&2; }
info() { printf "${CYAN}  %s${NC}\n" "$*"; }

# ============================================================
# Termux 环境检测
# ============================================================

IS_TERMUX=0
IS_PROOT=0
ARCH=""
BIN_FILE=""

detect_env() {
    # 检测 Termux：PREFIX 含 com.termux
    if [ -n "${PREFIX:-}" ] && [[ "$PREFIX" == *"com.termux"* ]]; then
        IS_TERMUX=1
    fi

    # 检测 proot（Termux 里跑 Linux 容器）
    if [ -f "/.proot" ] || grep -q "proot" /proc/version 2>/dev/null; then
        IS_PROOT=1
    fi

    uname_m="$(uname -m)"
    case "$uname_m" in
        aarch64|arm64)  ARCH="linux-arm64" ;;
        armv7l|armv8l|arm)  ARCH="linux-armv7" ;;
        x86_64|amd64)   ARCH="linux-amd64" ;;
        i686|i386)      ARCH="linux-386" ;;
        *) err "不支持的架构: $uname_m"; exit 1 ;;
    esac

    BIN_FILE="${BIN_NAME}-${ARCH}"

    log "环境: $(if [ "$IS_TERMUX" = "1" ]; then echo "Termux"; elif [ "$IS_PROOT" = "1" ]; then echo "proot Linux"; else echo "Linux"; fi)"
    log "架构: $uname_m → $ARCH"
}

# ============================================================
# 依赖检查（ca-certificates 对 WSS 至关重要）
# ============================================================

ensure_ca_certs() {
    if [ "$IS_TERMUX" != "1" ]; then
        return 0
    fi

    # 检测 CA 证书是否存在
    local ca_paths=(
        "$PREFIX/etc/tls/cert.pem"
        "$PREFIX/etc/ca-certificates/ca-bundle.crt"
        "$PREFIX/etc/ssl/certs/ca-certificates.crt"
    )

    for p in "${ca_paths[@]}"; do
        if [ -f "$p" ] && [ -s "$p" ]; then
            log "CA 证书已存在: $p"
            return 0
        fi
    done

    warn "未找到 CA 证书（WSS 连接会失败），正在安装 ca-certificates..."
    if command -v pkg >/dev/null 2>&1; then
        pkg install -y ca-certificates 2>&1 | tail -3 || true
    fi

    # 再次检查
    for p in "${ca_paths[@]}"; do
        if [ -f "$p" ] && [ -s "$p" ]; then
            ok "CA 证书安装成功: $p"
            return 0
        fi
    done

    warn "CA 证书安装失败，WSS 可能报 x509 错误"
    warn "手动修复: pkg install ca-certificates"
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

    local latest=""
    if command -v curl >/dev/null 2>&1; then
        latest=$(curl -fsSL --connect-timeout 8 --max-time 20 \
            "${GITHUB_API}/releases/latest" 2>/dev/null \
            | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' \
            | head -1 || true)
    elif command -v wget >/dev/null 2>&1; then
        latest=$(wget -qO- --timeout=20 "${GITHUB_API}/releases/latest" 2>/dev/null \
            | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' \
            | head -1 || true)
    fi

    if [ -n "$latest" ]; then
        echo "$latest"
    else
        echo "$FALLBACK_VERSION"
    fi
}

# ============================================================
# 停止运行中的客户端
# ============================================================

stop_running_client() {
    local stopped=0

    # 方法 1：PID 文件
    if [ -f "$PID_FILE" ]; then
        local pid
        pid=$(cat "$PID_FILE" 2>/dev/null | tr -d '[:space:]')
        if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
            log "停止运行中的客户端 (PID: $pid)"
            kill "$pid" 2>/dev/null || true
            sleep 1
            if kill -0 "$pid" 2>/dev/null; then
                warn "SIGTERM 未生效，使用 SIGKILL"
                kill -9 "$pid" 2>/dev/null || true
                sleep 0.5
            fi
            stopped=1
        fi
        rm -f "$PID_FILE"
    fi

    # 方法 2：pgrep（兜底，处理 PID 文件丢失的情况）
    if command -v pgrep >/dev/null 2>&1; then
        local pids
        pids=$(pgrep -f "${INSTALL_DIR}/${BIN_NAME}" 2>/dev/null || true)
        if [ -n "$pids" ]; then
            log "发现残留进程 (PID: $pids)，停止..."
            # shellcheck disable=SC2086
            kill $pids 2>/dev/null || true
            sleep 1
            pids=$(pgrep -f "${INSTALL_DIR}/${BIN_NAME}" 2>/dev/null || true)
            if [ -n "$pids" ]; then
                # shellcheck disable=SC2086
                kill -9 $pids 2>/dev/null || true
            fi
            stopped=1
        fi
    fi

    if [ "$stopped" = "1" ]; then
        ok "旧进程已停止"
    fi
}

# ============================================================
# 下载二进制
# ============================================================

install_binary() {
    local version="$1"
    mkdir -p "$INSTALL_DIR"

    local url="${GITHUB_DL}/${version}/${BIN_FILE}"
    log "下载: $url"

    local tmp="${INSTALL_DIR}/.${BIN_NAME}.tmp.$$"
    trap 'rm -f "$tmp"' EXIT

    if command -v curl >/dev/null 2>&1; then
        if ! curl -fL --connect-timeout 10 --max-time 300 \
            -o "$tmp" "$url" 2>&1; then
            err "下载失败: $url"
            err "请确认版本 $version 是否包含 $ARCH 二进制"
            exit 1
        fi
    elif command -v wget >/dev/null 2>&1; then
        if ! wget -O "$tmp" "$url"; then
            err "下载失败: $url"
            exit 1
        fi
    else
        err "curl 和 wget 都不可用"
        info "安装: pkg install curl"
        exit 1
    fi

    chmod +x "$tmp"
    mv "$tmp" "$BIN_PATH"
    trap - EXIT

    ok "已安装: $BIN_PATH ($(du -h "$BIN_PATH" | cut -f1))"
}

# ============================================================
# 交互式配置
# ============================================================

prompt_config() {
    if [ "$NO_LAUNCH" = "1" ]; then
        return 0
    fi

    # 已有 config.env 且没传 SIGNALING_URL → 用现成的
    if [ -f "$CONFIG_FILE" ] && [ -z "$SIGNALING_URL" ]; then
        log "使用已有配置: $CONFIG_FILE"
        set -a
        # shellcheck disable=SC1090
        source "$CONFIG_FILE"
        set +a
        return 0
    fi

    echo ""
    log "配置客户端（直接回车用默认值）"

    if [ -z "$SIGNALING_URL" ]; then
        printf "SIGNALING_URL (wss://xxx.workers.dev): "
        read -r SIGNALING_URL || true
    fi

    if [ -z "$SIGNALING_URL" ]; then
        warn "未填写 SIGNALING_URL，跳过启动"
        NO_LAUNCH=1
        return 0
    fi

    if [ -z "$ROOM_ID" ]; then
        printf "ROOM_ID [default-room]: "
        read -r input || true
        ROOM_ID="${input:-default-room}"
    fi

    if [ -z "$CLIENT_ID" ]; then
        # 已有持久化的 client_id 就复用
        if [ -f "$INSTALL_DIR/client_id" ]; then
            CLIENT_ID=$(cat "$INSTALL_DIR/client_id" 2>/dev/null | tr -d '[:space:]')
            if [ -n "$CLIENT_ID" ]; then
                log "复用已有 CLIENT_ID: $CLIENT_ID"
            fi
        fi
        if [ -z "$CLIENT_ID" ]; then
            printf "CLIENT_ID [自动生成]: "
            read -r input || true
            CLIENT_ID="$input"
        fi
    fi

    if [ -z "$NODE_NAME" ]; then
        hostname_default=$(hostname 2>/dev/null || echo "termux")
        printf "NODE_NAME [$hostname_default]: "
        read -r input || true
        NODE_NAME="${input:-$hostname_default}"
    fi

    if [ -z "$CONNECT_TOKEN" ]; then
        printf "CONNECT_TOKEN [留空]: "
        read -rs input || true
        echo
        CONNECT_TOKEN="$input"
    fi

    if [ -z "$SOCKS5_LISTEN" ]; then
        printf "SOCKS5_LISTEN [127.0.0.1:1080]: "
        read -r input || true
        SOCKS5_LISTEN="${input:-127.0.0.1:1080}"
    fi

    return 0
}

save_config() {
    mkdir -p "$INSTALL_DIR"
    cat > "$CONFIG_FILE" <<EOF
# n2n-termux 配置
# 由 install.sh 生成于 $(date -Iseconds 2>/dev/null || date)

SIGNALING_URL="$(printf '%s' "${SIGNALING_URL:-}" | sed 's/"/\\"/g')"
CONNECT_TOKEN="$(printf '%s' "${CONNECT_TOKEN:-}" | sed 's/"/\\"/g')"
ROOM_ID="$(printf '%s' "${ROOM_ID:-default-room}" | sed 's/"/\\"/g')"
CLIENT_ID="$(printf '%s' "${CLIENT_ID:-}" | sed 's/"/\\"/g')"
NODE_NAME="$(printf '%s' "${NODE_NAME:-termux}" | sed 's/"/\\"/g')"
SOCKS5_LISTEN="$(printf '%s' "${SOCKS5_LISTEN:-127.0.0.1:1080}" | sed 's/"/\\"/g')"
UDP_PORT="${UDP_PORT:-0}"
STUN_SERVERS="$(printf '%s' "${STUN_SERVERS:-}" | sed 's/"/\\"/g')"
EOF
    chmod 600 "$CONFIG_FILE"
    ok "配置已保存: $CONFIG_FILE"
}

# ============================================================
# 生成启动 / 停止脚本
# ============================================================

write_launcher() {
    cat > "$LAUNCHER" <<'EOF'
#!/usr/bin/env bash
# n2n-termux 前台启动脚本（Ctrl+C 停止）
set -euo pipefail

INSTALL_DIR="${INSTALL_DIR:-$HOME/.n2n-termux}"
CONFIG_FILE="$INSTALL_DIR/config.env"
BIN="$INSTALL_DIR/n2n-client"

if [ ! -f "$CONFIG_FILE" ]; then
    echo "配置文件不存在: $CONFIG_FILE"
    exit 1
fi
if [ ! -x "$BIN" ]; then
    echo "二进制不存在: $BIN"
    exit 1
fi

set -a
# shellcheck disable=SC1090
source "$CONFIG_FILE"
set +a

exec "$BIN" "$@"
EOF
    chmod +x "$LAUNCHER"
    ok "前台启动脚本: $LAUNCHER"
}

write_background_launcher() {
    cat > "$INSTALL_DIR/background.sh" <<'EOF'
#!/usr/bin/env bash
# n2n-termux 后台启动脚本（nohup）
set -euo pipefail

INSTALL_DIR="${INSTALL_DIR:-$HOME/.n2n-termux}"
CONFIG_FILE="$INSTALL_DIR/config.env"
BIN="$INSTALL_DIR/n2n-client"
PID_FILE="$INSTALL_DIR/n2n.pid"
LOG_FILE="$INSTALL_DIR/n2n.log"

if [ ! -f "$CONFIG_FILE" ]; then
    echo "配置文件不存在: $CONFIG_FILE"
    exit 1
fi
if [ ! -x "$BIN" ]; then
    echo "二进制不存在: $BIN"
    exit 1
fi

# 已在运行？
if [ -f "$PID_FILE" ]; then
    oldpid=$(cat "$PID_FILE" 2>/dev/null | tr -d '[:space:]')
    if [ -n "$oldpid" ] && kill -0 "$oldpid" 2>/dev/null; then
        echo "已在运行 (PID: $oldpid)"
        exit 0
    fi
    rm -f "$PID_FILE"
fi

set -a
# shellcheck disable=SC1090
source "$CONFIG_FILE"
set +a

# 日志轮转：超过 5MB 时备份
if [ -f "$LOG_FILE" ] && [ "$(stat -c%s "$LOG_FILE" 2>/dev/null || echo 0)" -gt 5242880 ]; then
    mv "$LOG_FILE" "$LOG_FILE.old"
fi

nohup "$BIN" >> "$LOG_FILE" 2>&1 &
NEW_PID=$!
echo "$NEW_PID" > "$PID_FILE"
echo "已后台启动 (PID: $NEW_PID)"
echo "日志: tail -f $LOG_FILE"
EOF
    chmod +x "$INSTALL_DIR/background.sh"
    ok "后台启动脚本: $INSTALL_DIR/background.sh"
}

write_stopper() {
    cat > "$STOPPER" <<'EOF'
#!/usr/bin/env bash
# n2n-termux 停止脚本
set -euo pipefail

INSTALL_DIR="${INSTALL_DIR:-$HOME/.n2n-termux}"
PID_FILE="$INSTALL_DIR/n2n.pid"

stopped=0

if [ -f "$PID_FILE" ]; then
    pid=$(cat "$PID_FILE" 2>/dev/null | tr -d '[:space:]')
    if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
        echo "停止中 (PID: $pid)..."
        kill "$pid" 2>/dev/null || true
        sleep 1
        if kill -0 "$pid" 2>/dev/null; then
            kill -9 "$pid" 2>/dev/null || true
        fi
        stopped=1
    fi
    rm -f "$PID_FILE"
fi

# 兜底 pgrep
if command -v pgrep >/dev/null 2>&1; then
    pids=$(pgrep -f "$INSTALL_DIR/n2n-client" 2>/dev/null || true)
    if [ -n "$pids" ]; then
        kill $pids 2>/dev/null || true
        sleep 1
        pids=$(pgrep -f "$INSTALL_DIR/n2n-client" 2>/dev/null || true)
        [ -n "$pids" ] && kill -9 $pids 2>/dev/null || true
        stopped=1
    fi
fi

if [ "$stopped" = "1" ]; then
    echo "已停止"
else
    echo "未在运行"
fi
EOF
    chmod +x "$STOPPER"
    ok "停止脚本: $STOPPER"
}

# ============================================================
# 启动客户端
# ============================================================

launch_client() {
    if [ "$NO_LAUNCH" = "1" ]; then
        echo ""
        ok "安装完成"
        info "前台启动: $LAUNCHER"
        info "后台启动: $INSTALL_DIR/background.sh"
        info "停止:     $STOPPER"
        return 0
    fi

    if [ "$AUTO_START" != "1" ]; then
        echo ""
        printf "是否立即启动客户端？[Y/n] "
        read -r answer || true
        case "$answer" in
            n|N|no|No|NO)
                ok "安装完成（未启动）"
                info "前台启动: $LAUNCHER"
                info "后台启动: $INSTALL_DIR/background.sh"
                return 0
                ;;
        esac
    fi

    if [ -z "$SIGNALING_URL" ]; then
        err "未设置 SIGNALING_URL，无法启动"
        info "手动: SIGNALING_URL=wss://... $LAUNCHER"
        return 1
    fi

    echo ""
    if [ "$AUTO_START" = "1" ]; then
        # AUTO_START=1 时静默后台启动
        log "后台启动..."
        INSTALL_DIR="$INSTALL_DIR" "$INSTALL_DIR/background.sh"
        sleep 2
        info "日志: tail -f $LOG_FILE"
    else
        # 交互模式前台启动（能看到日志）
        ok "启动中（Ctrl+C 停止）..."
        exec "$LAUNCHER"
    fi
}

# ============================================================
# 主流程
# ============================================================

main() {
    echo ""
    echo "========================================================"
    echo "  n2n-termux 一键部署"
    echo "  https://github.com/${REPO}"
    echo "========================================================"
    echo ""

    detect_env
    ensure_ca_certs

    local installed latest need_update
    installed="$(read_installed_version)"

    log "查询最新版本..."
    latest="$(resolve_latest_version)"

    if [ -n "$installed" ]; then
        log "本地已装: $installed"
    else
        log "本地未装（或旧版无版本记录）"
    fi
    log "远端最新: $latest"

    need_update=1
    if [ -n "$installed" ] && [ "$installed" = "$latest" ] && [ "$FORCE_UPDATE" != "1" ] \
        && [ -f "$BIN_PATH" ] && [ -x "$BIN_PATH" ]; then
        need_update=0
    fi

    if [ "$CHECK_ONLY" = "1" ]; then
        echo ""
        if [ "$need_update" = "1" ]; then
            ok "有新版本可更新: ${installed:-无} → $latest"
            info "运行不带 CHECK_ONLY=1 即可升级"
        else
            ok "已是最新版本: $latest"
        fi
        exit 0
    fi

    if [ "$need_update" = "0" ]; then
        ok "已是最新版本: $latest（跳过下载，FORCE_UPDATE=1 可强制重装）"
    else
        if [ -n "$installed" ]; then
            log "升级中: $installed → $latest"
        else
            log "首次安装: $latest"
        fi

        stop_running_client
        install_binary "$latest"
        write_installed_version "$latest"
        ok "版本已更新: $latest"
    fi

    # 配置
    prompt_config
    if [ -n "$SIGNALING_URL" ]; then
        save_config
    fi

    # 持久化 client_id（Go 层首次启动会自己写，这里先写一份也行）
    if [ -n "$CLIENT_ID" ] && [ "$CLIENT_ID" != "" ]; then
        mkdir -p "$INSTALL_DIR"
        echo "$CLIENT_ID" > "$INSTALL_DIR/client_id"
        chmod 600 "$INSTALL_DIR/client_id"
    fi

    write_launcher
    write_background_launcher
    write_stopper

    echo ""
    ok "部署完成！"
    echo ""
    info "二进制:   $BIN_PATH"
    info "版本:     $latest"
    info "配置:     $CONFIG_FILE"
    info "日志:     $LOG_FILE"
    info "前台启动: $LAUNCHER"
    info "后台启动: $INSTALL_DIR/background.sh"
    info "停止:     $STOPPER"
    echo ""

    launch_client
}

main "$@"
