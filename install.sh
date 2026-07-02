#!/bin/bash
#
# Install script for git-spice (gs).
# This script installs gs from source and configures PATH in the
# appropriate shell rc file (.zshrc for zsh, .bashrc for bash).
#
# Usage:
#   ./install.sh
#
# Requirements:
#   - curl or wget (used to download Go if not already installed)
#

set -euo pipefail

# Colors for output (disabled if not a terminal).
if [ -t 1 ]; then
    RED='\033[0;31m'
    GREEN='\033[0;32m'
    YELLOW='\033[0;33m'
    NC='\033[0m' # No Color
else
    RED=''
    GREEN=''
    YELLOW=''
    NC=''
fi

info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

error() {
    echo -e "${RED}[ERROR]${NC} $1" >&2
}

die() {
    error "$1"
    exit 1
}

# Detect OS and CPU architecture for Go tarball selection.
detect_platform() {
    local os arch

    case "$(uname -s)" in
        Linux)  os="linux" ;;
        Darwin) os="darwin" ;;
        *)
            die "Unsupported OS: $(uname -s). Install Go manually: https://go.dev/dl"
            ;;
    esac

    case "$(uname -m)" in
        x86_64)        arch="amd64" ;;
        aarch64|arm64) arch="arm64" ;;
        *)
            die "Unsupported architecture: $(uname -m). Install Go manually: https://go.dev/dl"
            ;;
    esac

    echo "${os}-${arch}"
}

# Fetch the latest stable Go version string (e.g., "go1.24.0").
# Falls back to go1.21.0 if the fetch fails.
get_latest_go_version() {
    local version
    if command -v curl &> /dev/null; then
        version=$(curl -fsSL "https://go.dev/VERSION?m=text" 2>/dev/null \
            | head -1 | tr -d '[:space:]')
    elif command -v wget &> /dev/null; then
        version=$(wget -qO- "https://go.dev/VERSION?m=text" 2>/dev/null \
            | head -1 | tr -d '[:space:]')
    fi
    echo "${version:-go1.21.0}"
}

# Install Go to /usr/local/go from the official download site.
install_go() {
    local version platform
    version=$(get_latest_go_version)
    platform=$(detect_platform)

    info "Installing $version for ${platform}..."

    # Determine whether sudo is needed to write to /usr/local.
    local use_sudo=""
    if [ ! -w "/usr/local" ]; then
        if ! command -v sudo &> /dev/null; then
            die "Cannot write to /usr/local and sudo is unavailable. Install Go manually: https://go.dev/dl"
        fi
        use_sudo="sudo"
        warn "Installing Go to /usr/local/go requires sudo."
    fi

    # Download the tarball to a temporary directory.
    local tmp_dir tarball
    tmp_dir=$(mktemp -d)
    # shellcheck disable=SC2064
    trap "rm -rf '$tmp_dir'" EXIT
    tarball="$tmp_dir/go.tar.gz"

    local tarball_url="https://go.dev/dl/${version}.${platform}.tar.gz"
    info "Downloading $tarball_url..."

    if command -v curl &> /dev/null; then
        curl -fsSL -o "$tarball" "$tarball_url"
    elif command -v wget &> /dev/null; then
        wget -qO "$tarball" "$tarball_url"
    else
        die "Neither curl nor wget is available. Install Go manually: https://go.dev/dl"
    fi

    # Remove any existing Go installation before extracting.
    if [ -d "/usr/local/go" ]; then
        info "Removing existing Go installation at /usr/local/go..."
        $use_sudo rm -rf "/usr/local/go"
    fi

    $use_sudo tar -C /usr/local -xzf "$tarball"

    # Make Go available in the current shell session.
    export PATH="/usr/local/go/bin:$PATH"

    info "Go installed at /usr/local/go."
}

# Check if Go is installed; install it automatically if not.
check_go() {
    if ! command -v go &> /dev/null; then
        warn "Go is not installed."
        install_go
    fi

    # Verify Go version is at least 1.21.
    local go_version
    go_version=$(go version | grep -oE 'go[0-9]+\.[0-9]+' | sed 's/go//')
    local major minor
    major=$(echo "$go_version" | cut -d. -f1)
    minor=$(echo "$go_version" | cut -d. -f2)

    if [ "$major" -lt 1 ] || { [ "$major" -eq 1 ] && [ "$minor" -lt 21 ]; }; then
        die "Go version 1.21 or later is required. Found: go$go_version"
    fi

    info "Found Go version: go$go_version"
}

# Determine GOBIN directory.
# Priority:
#   1. $GOBIN if set
#   2. $GOPATH/bin if GOPATH is set
#   3. $HOME/go/bin (Go default)
get_gobin() {
    if [ -n "${GOBIN:-}" ]; then
        echo "$GOBIN"
    elif [ -n "${GOPATH:-}" ]; then
        echo "$GOPATH/bin"
    else
        echo "$HOME/go/bin"
    fi
}

# Install gs using go install.
install_gs() {
    info "Installing git-spice..."

    # Stamp the installed commit into the binary so that gs can detect
    # when a newer version is available on main and offer to self-update.
    local ldflags=""
    if command -v git &> /dev/null && git rev-parse --git-dir &> /dev/null; then
        ldflags="-X main._commit=$(git rev-parse HEAD)"
    fi

    go install -v -tags="" -ldflags="$ldflags" go.abhg.dev/gs
    info "git-spice installed successfully."
}

# Detect the user's login shell.
# When running under sudo, $SHELL reflects root's shell, not the user's.
# This function tries multiple methods to find the real user's shell.
get_user_shell() {
    local real_user shell_path

    # Determine the real user (handles sudo).
    # Fall back to id -un when USER is unset (e.g., in Docker containers).
    real_user="${SUDO_USER:-${USER:-$(id -un)}}"

    # Try to get login shell from passwd database.
    # Works on Linux (getent) and macOS (dscl).
    if command -v getent &> /dev/null; then
        shell_path=$(getent passwd "$real_user" | cut -d: -f7)
    elif command -v dscl &> /dev/null; then
        shell_path=$(dscl . -read "/Users/$real_user" UserShell 2>/dev/null \
            | awk '{print $2}')
    fi

    # Fall back to $SHELL if lookup failed.
    echo "${shell_path:-${SHELL:-/bin/bash}}"
}

# Detect the user's shell and return the appropriate rc file.
get_shell_rc() {
    local shell_path shell_name real_user home_dir

    shell_path=$(get_user_shell)
    shell_name=$(basename "$shell_path")

    # Determine the real user's home directory (handles sudo).
    # Fall back to id -un when USER is unset (e.g., in Docker containers).
    real_user="${SUDO_USER:-${USER:-$(id -un)}}"
    if [ -n "${SUDO_USER:-}" ]; then
        # Get home directory for the real user, not root.
        if command -v getent &> /dev/null; then
            home_dir=$(getent passwd "$real_user" | cut -d: -f6)
        elif command -v dscl &> /dev/null; then
            home_dir=$(dscl . -read "/Users/$real_user" NFSHomeDirectory \
                2>/dev/null | awk '{print $2}')
        fi
        home_dir="${home_dir:-$HOME}"
    else
        home_dir="$HOME"
    fi

    case "$shell_name" in
        zsh)
            echo "$home_dir/.zshrc"
            ;;
        bash)
            echo "$home_dir/.bashrc"
            ;;
        *)
            # Default to .bashrc for unknown shells.
            warn "Unknown shell '$shell_name', defaulting to .bashrc"
            echo "$home_dir/.bashrc"
            ;;
    esac
}

# Add GOBIN to PATH in the shell rc file if not already present.
configure_shell_rc() {
    local gobin="$1"
    local rc_file
    rc_file=$(get_shell_rc)
    local rc_name
    rc_name=$(basename "$rc_file")
    local export_line="export PATH=\"$gobin:\$PATH\""

    # Check if rc file exists.
    if [ ! -f "$rc_file" ]; then
        warn "$rc_name not found. Creating it."
        touch "$rc_file"
    fi

    # Check if GOBIN is already in PATH via rc file.
    if grep -qF "$gobin" "$rc_file" 2>/dev/null; then
        info "GOBIN ($gobin) is already configured in $rc_name"
        return
    fi

    # Append export line to rc file.
    {
        echo ""
        echo "# Added by git-spice install script"
        echo "$export_line"
    } >> "$rc_file"

    info "Added GOBIN to PATH in $rc_name"
    warn "Run 'source ~/$rc_name' or start a new shell to update your PATH."
}

# Add /usr/local/go/bin to PATH in the shell rc file if not already present.
configure_go_rc() {
    local go_bin="/usr/local/go/bin"
    local rc_file
    rc_file=$(get_shell_rc)
    local rc_name
    rc_name=$(basename "$rc_file")

    if [ ! -f "$rc_file" ]; then
        warn "$rc_name not found. Creating it."
        touch "$rc_file"
    fi

    if grep -qF "$go_bin" "$rc_file" 2>/dev/null; then
        info "Go bin ($go_bin) is already configured in $rc_name"
        return
    fi

    {
        echo ""
        echo "# Added by git-spice install script"
        echo "export PATH=\"$go_bin:\$PATH\""
    } >> "$rc_file"

    info "Added Go bin to PATH in $rc_name"
    warn "Run 'source ~/$rc_name' or start a new shell to update your PATH."
}

# Check if gs is accessible in PATH.
verify_installation() {
    local gobin="$1"

    if [ -x "$gobin/gs" ]; then
        info "gs binary installed at: $gobin/gs"
    else
        warn "gs binary not found at expected location: $gobin/gs"
    fi
}

main() {
    info "Starting git-spice installation..."

    # Track whether Go needs to be installed.
    local go_was_missing=false
    if ! command -v go &> /dev/null; then
        go_was_missing=true
    fi

    check_go

    # Configure the shell rc to include the Go toolchain if just installed.
    if [ "$go_was_missing" = "true" ]; then
        configure_go_rc
    fi

    local gobin
    gobin=$(get_gobin)
    info "GOBIN directory: $gobin"

    install_gs
    configure_shell_rc "$gobin"
    verify_installation "$gobin"

    echo ""
    info "Installation complete!"
    info "Run 'gs --help' to get started."
}

main "$@"
