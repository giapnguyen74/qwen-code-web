#!/bin/bash
set -e

# Define colors
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo -e "${BLUE}Building qwen-code-web...${NC}"
go build -ldflags="-s -w" -o qwen-code-web .

echo -e "${BLUE}Installing to ~/.local/bin/...${NC}"
mkdir -p ~/.local/bin
cp qwen-code-web ~/.local/bin/

echo -e "${GREEN}Successfully installed qwen-code-web!${NC}"
echo ""
echo -e "${YELLOW}To start the server, we recommend running it in a tmux session so the TUI stays alive:${NC}"
echo ""
echo "  tmux new -A -s qwen"
echo "  cd <your-workspace-dir>"
echo "  qwen-code-web"
echo ""
echo -e "${YELLOW}Note:${NC} If this is your first time, run 'qwen-code-web --password' to set up your web dashboard password!"
