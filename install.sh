#!/usr/bin/env bash
# qwen-code-web installer
# Usage: curl -fsSL https://raw.githubusercontent.com/YOUR_USER/qwen-code-web/main/install.sh | bash
set -euo pipefail

# ── Config ────────────────────────────────────────────────────────────────────
REPO_URL="${QWEN_WEB_REPO:-https://github.com/YOUR_USER/qwen-code-web.git}"
INSTALL_DIR="${QWEN_WEB_DIR:-${HOME}/.local/share/qwen-code-web}"
BIN_DIR="${QWEN_WEB_BIN:-${HOME}/.local/bin}"
BIN_PATH="${BIN_DIR}/qwen-code-web"
NODE_MIN_MAJOR=18

# ── Colours ───────────────────────────────────────────────────────────────────
if [ -t 1 ]; then
  BOLD='\033[1m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
  RED='\033[0;31m'; CYAN='\033[0;36m'; DIM='\033[2m'; NC='\033[0m'
else
  BOLD=''; GREEN=''; YELLOW=''; RED=''; CYAN=''; DIM=''; NC=''
fi

step()  { echo -e "${CYAN}${BOLD}==> ${NC}${BOLD}${1}${NC}"; }
ok()    { echo -e "    ${GREEN}✓${NC} ${1}"; }
warn()  { echo -e "    ${YELLOW}!${NC} ${1}"; }
die()   { echo -e "\n${RED}${BOLD}Error:${NC} ${1}\n" >&2; exit 1; }

# ── Prerequisite checks ───────────────────────────────────────────────────────
step "Checking prerequisites"

# git
command -v git >/dev/null 2>&1 || die "git is required but not found. Install git and retry."
ok "git $(git --version | awk '{print $3}')"

# node
command -v node >/dev/null 2>&1 || die "Node.js is required but not found.
  Install it via nvm: https://github.com/nvm-sh/nvm
  Or from: https://nodejs.org"

NODE_VERSION=$(node --version | sed 's/v//')
NODE_MAJOR=$(echo "${NODE_VERSION}" | cut -d. -f1)
if [ "${NODE_MAJOR}" -lt "${NODE_MIN_MAJOR}" ]; then
  die "Node.js ${NODE_MIN_MAJOR}+ is required, found v${NODE_VERSION}."
fi
ok "node v${NODE_VERSION}"

# npm
command -v npm >/dev/null 2>&1 || die "npm is required but not found."
ok "npm $(npm --version)"

# C++ compiler (needed for node-pty native module)
if command -v cc >/dev/null 2>&1; then
  ok "C compiler found ($(cc --version 2>&1 | head -1))"
else
  warn "No C compiler found. node-pty native build may fail."
  if [ "$(uname)" = "Darwin" ]; then
    warn "On macOS run: xcode-select --install"
  else
    warn "On Debian/Ubuntu run: sudo apt install build-essential"
  fi
fi

# python3 (node-gyp dependency)
if command -v python3 >/dev/null 2>&1; then
  ok "python3 $(python3 --version 2>&1 | awk '{print $2}')"
else
  warn "python3 not found. node-gyp (node-pty build) may fail."
fi

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
  step "Installing to ${INSTALL_DIR}"
  mkdir -p "$(dirname "${INSTALL_DIR}")"
  git clone --depth=1 "${REPO_URL}" "${INSTALL_DIR}"
  ok "Cloned $(git -C "${INSTALL_DIR}" rev-parse --short HEAD)"
fi

# ── Install dependencies ──────────────────────────────────────────────────────
step "Installing npm dependencies (including native node-pty build)"
cd "${INSTALL_DIR}"
npm install --prefer-offline --no-audit --no-fund 2>&1 \
  | grep -E '^(added|updated|npm (warn|error))' || true
ok "Dependencies installed"

# ── Build TypeScript ──────────────────────────────────────────────────────────
step "Building"
npm run build
ok "Build complete"

# ── Install bin wrapper ───────────────────────────────────────────────────────
step "Installing binary to ${BIN_PATH}"
mkdir -p "${BIN_DIR}"

# The wrapper uses the same node that's in PATH at run-time (respects nvm).
cat > "${BIN_PATH}" <<EOF
#!/usr/bin/env bash
INSTALL_DIR="${INSTALL_DIR}"
exec node "\${INSTALL_DIR}/dist/cli.js" "\$@"
EOF
chmod +x "${BIN_PATH}"
ok "Wrote ${BIN_PATH}"

# ── PATH setup ────────────────────────────────────────────────────────────────
step "Checking PATH"

add_to_path() {
  local FILE="$1"
  local LINE='export PATH="${HOME}/.local/bin:${PATH}"'
  if [ -f "${FILE}" ] && grep -q '\.local/bin' "${FILE}" 2>/dev/null; then
    ok "${FILE} already has ~/.local/bin in PATH"
  else
    echo "" >> "${FILE}"
    echo "# Added by qwen-code-web installer" >> "${FILE}"
    echo "${LINE}" >> "${FILE}"
    ok "Added ~/.local/bin to PATH in ${FILE}"
  fi
}

if echo ":${PATH}:" | grep -q ":${BIN_DIR}:"; then
  ok "~/.local/bin is already in PATH"
else
  warn "~/.local/bin is not in PATH — adding now"

  SHELL_NAME=$(basename "${SHELL:-/bin/sh}")
  case "${SHELL_NAME}" in
    zsh)   add_to_path "${HOME}/.zshrc" ;;
    bash)
      # macOS uses .bash_profile for login shells, Linux uses .bashrc
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
        echo "" >> "${FISH_CONFIG}"
        echo "# Added by qwen-code-web installer" >> "${FISH_CONFIG}"
        echo 'fish_add_path "$HOME/.local/bin"' >> "${FISH_CONFIG}"
        ok "Added ~/.local/bin to PATH in ${FISH_CONFIG}"
      fi
      ;;
    *)
      warn "Unknown shell '${SHELL_NAME}'. Add this line to your shell config manually:"
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
echo -e "  Location: ${DIM}${INSTALL_DIR}${NC}"
echo ""
echo -e "  ${BOLD}Usage:${NC}"
echo -e "    ${CYAN}qwen-code-web --project-dir ./my-project${NC}"
echo -e "    ${CYAN}qwen-code-web --project-dir ./my-project --resume${NC}"
echo ""
