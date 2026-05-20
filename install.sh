#!/usr/bin/env bash
# qwen-code-web installer
# Usage: curl -fsSL https://raw.githubusercontent.com/YOUR_USER/qwen-code-web/main/install.sh | bash
set -euo pipefail

# ── Config ────────────────────────────────────────────────────────────────────
REPO_URL="${QWEN_WEB_REPO:-https://github.com/YOUR_USER/qwen-code-web.git}"
INSTALL_DIR="${QWEN_WEB_DIR:-${HOME}/.local/share/qwen-code-web}"
BIN_DIR="${QWEN_WEB_BIN:-${HOME}/.local/bin}"
BIN_PATH="${BIN_DIR}/qwen-code-web"
GO_MIN_MAJOR=1
GO_MIN_MINOR=21

# ── Colours ───────────────────────────────────────────────────────────────────
if [ -t 1 ]; then
  BOLD='\033[1m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
  RED='\033[0;31m'; CYAN='\033[0;36m'; DIM='\033[2m'; NC='\033[0m'
else
  BOLD=''; GREEN=''; YELLOW=''; RED=''; CYAN=''; DIM=''; NC=''
fi

step() { echo -e "${CYAN}${BOLD}==> ${NC}${BOLD}${1}${NC}"; }
ok()   { echo -e "    ${GREEN}✓${NC} ${1}"; }
warn() { echo -e "    ${YELLOW}!${NC} ${1}"; }
die()  { echo -e "\n${RED}${BOLD}Error:${NC} ${1}\n" >&2; exit 1; }

# ── Prerequisite checks ───────────────────────────────────────────────────────
step "Checking prerequisites"

command -v git >/dev/null 2>&1 || die "git is required. Install it and retry."
ok "git $(git --version | awk '{print $3}')"

# Go — required to build
if ! command -v go >/dev/null 2>&1; then
  die "Go is required but not found.
  Install it from: https://go.dev/dl/
  Or via your package manager:
    macOS:  brew install go
    Ubuntu: sudo apt install golang-go"
fi

GO_VERSION=$(go version | awk '{print $3}' | sed 's/go//')
GO_MAJOR=$(echo "${GO_VERSION}" | cut -d. -f1)
GO_MINOR=$(echo "${GO_VERSION}" | cut -d. -f2)
if [ "${GO_MAJOR}" -lt "${GO_MIN_MAJOR}" ] || \
   { [ "${GO_MAJOR}" -eq "${GO_MIN_MAJOR}" ] && [ "${GO_MINOR}" -lt "${GO_MIN_MINOR}" ]; }; then
  die "Go ${GO_MIN_MAJOR}.${GO_MIN_MINOR}+ required, found ${GO_VERSION}."
fi
ok "go ${GO_VERSION}"

# ── Clone or update ───────────────────────────────────────────────────────────
if [ -d "${INSTALL_DIR}/.git" ]; then
  step "Updating existing installation at ${INSTALL_DIR}"
  git -C "${INSTALL_DIR}" fetch --quiet origin
  LOCAL=$(git -C "${INSTALL_DIR}" rev-parse HEAD)
  REMOTE=$(git -C "${INSTALL_DIR}" rev-parse origin/HEAD 2>/dev/null \
           || git -C "${INSTALL_DIR}" rev-parse origin/main 2>/dev/null \
           || git -C "${INSTALL_DIR}" rev-parse origin/master 2>/dev/null)
  if [ "${LOCAL}" = "${REMOTE}" ]; then
    ok "Already up to date (${LOCAL:0:7})"
  else
    git -C "${INSTALL_DIR}" pull --ff-only --quiet
    ok "Updated to $(git -C "${INSTALL_DIR}" rev-parse --short HEAD)"
  fi
else
  step "Cloning to ${INSTALL_DIR}"
  mkdir -p "$(dirname "${INSTALL_DIR}")"
  git clone --depth=1 "${REPO_URL}" "${INSTALL_DIR}"
  ok "Cloned $(git -C "${INSTALL_DIR}" rev-parse --short HEAD)"
fi

# ── Build ─────────────────────────────────────────────────────────────────────
step "Building (go build)"
cd "${INSTALL_DIR}"
go build -ldflags="-s -w" -o "${BIN_PATH}" .
ok "Built → ${BIN_PATH}"

# ── PATH setup ────────────────────────────────────────────────────────────────
step "Checking PATH"

add_to_path() {
  local FILE="$1"
  local LINE='export PATH="${HOME}/.local/bin:${PATH}"'
  if [ -f "${FILE}" ] && grep -q '\.local/bin' "${FILE}" 2>/dev/null; then
    ok "${FILE} already has ~/.local/bin in PATH"
  else
    { echo ""; echo "# Added by qwen-code-web installer"; echo "${LINE}"; } >> "${FILE}"
    ok "Added ~/.local/bin to PATH in ${FILE}"
  fi
}

if echo ":${PATH}:" | grep -q ":${BIN_DIR}:"; then
  ok "~/.local/bin is already in PATH"
else
  warn "~/.local/bin is not in PATH — adding now"
  SHELL_NAME=$(basename "${SHELL:-/bin/sh}")
  case "${SHELL_NAME}" in
    zsh)  add_to_path "${HOME}/.zshrc" ;;
    bash)
      if [ "$(uname)" = "Darwin" ]; then
        add_to_path "${HOME}/.bash_profile"
      else
        add_to_path "${HOME}/.bashrc"
      fi
      ;;
    fish)
      FISH_CONFIG="${HOME}/.config/fish/config.fish"
      mkdir -p "$(dirname "${FISH_CONFIG}")"
      if grep -q '\.local/bin' "${FISH_CONFIG}" 2>/dev/null; then
        ok "${FISH_CONFIG} already has ~/.local/bin in PATH"
      else
        { echo ""; echo "# Added by qwen-code-web installer"
          echo 'fish_add_path "$HOME/.local/bin"'; } >> "${FISH_CONFIG}"
        ok "Added ~/.local/bin to PATH in ${FISH_CONFIG}"
      fi
      ;;
    *)
      warn "Unknown shell '${SHELL_NAME}'. Add this to your shell config manually:"
      echo "    export PATH=\"\${HOME}/.local/bin:\${PATH}\""
      ;;
  esac
  echo ""
  warn "Reload your shell or run:"
  echo -e "    ${BOLD}export PATH=\"\${HOME}/.local/bin:\${PATH}\"${NC}"
fi

# ── Done ──────────────────────────────────────────────────────────────────────
echo ""
echo -e "${GREEN}${BOLD}✓ qwen-code-web installed successfully${NC}"
echo ""
echo -e "  Version : ${DIM}$(git -C "${INSTALL_DIR}" rev-parse --short HEAD)${NC}"
echo -e "  Binary  : ${DIM}${BIN_PATH}${NC}"
echo ""
echo -e "  ${BOLD}Usage:${NC}"
echo -e "    ${CYAN}qwen-code-web --project-dir ./my-project${NC}"
echo -e "    ${CYAN}qwen-code-web --project-dir ./my-project --resume${NC}"
echo ""
