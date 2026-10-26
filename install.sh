#!/bin/bash
# ============================================
# FLAC Server — Install Script for HG680P (Armbian)
# Run as root: bash <(curl ... ) OR paste manually
# ============================================

set -e

# Colors
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m'

echo -e "${GREEN}=== FLAC Server Install ===${NC}"
echo -e "${YELLOW}Target: /mnt/hdd/music${NC}"
echo ""

# --- Step 1: Install dependencies ---
echo -e "${GREEN}[1/6] Installing dependencies...${NC}"
apt update -qq
apt install -y -qq curl wget golang-go git
go version

# --- Step 2: Create directories ---
echo -e "${GREEN}[2/6] Creating directories...${NC}"
mkdir -p /mnt/hdd/music
mkdir -p /mnt/hdd/music_writable
mkdir -p /DATA/AppData/flac-server
mkdir -p /DATA/AppData/flac-server/art
mkdir -p /DATA/AppData/flac-server/tmp
mkdir -p /root/flac-server

# Check if music dir has content
if [ -n "$(ls /mnt/hdd/music/ 2>/dev/null)" ]; then
    echo -e "${GREEN}  Found music files in /mnt/hdd/music/${NC}"
    echo "  Files: $(ls /mnt/hdd/music/ | wc -l)"
else
    echo -e "${YELLOW}  WARNING: /mnt/hdd/music is empty. Upload FLAC files later.${NC}"
fi

# --- Step 3: Copy or download project ---
echo -e "${GREEN}[3/6] Setting up project...${NC}"
cd /root/flac-server

# If project already exists via scp/cp, skip download
if [ -f "/root/flac-server/cmd/server/main.go" ]; then
    echo -e "${GREEN}  Project already exists.${NC}"
else
    echo "  Downloading project..."
    wget -q https://github.com/username/flac-server/archive/refs/heads/main.tar.gz -O /tmp/flac-server.tar.gz
    tar -xzf /tmp/flac-server.tar.gz -C /root/
    mv /root/flac-server-main/* /root/flac-server/
    rm -rf /tmp/flac-server.tar.gz /root/flac-server-main
fi

# --- Step 4: Build binary ---
echo -e "${GREEN}[4/6] Building server binary...${NC}"
cd /root/flac-server
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o /usr/local/bin/flac-server ./cmd/server/
echo -e "${GREEN}  Binary built: /usr/local/bin/flac-server${NC}"

# --- Step 5: Create systemd service ---
echo -e "${GREEN}[5/6] Creating systemd service...${NC}"
cat > /etc/systemd/system/flac-server.service << 'EOF'
[Unit]
Description=FLAC Streaming Server
After=network.target sound.target
Wants=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/flac-server
Restart=always
RestartSec=5
WorkingDirectory=/root/flac-server
Environment=HOME=/root
Environment=CONFIG_PATH=/root/flac-server/config.yaml
StandardOutput=journal
StandardError=journal
SyslogIdentifier=flac-server

# Memory limits
Environment=GOMEMLIMIT=80MiB
Environment=GOGC=50

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable flac-server
systemctl start flac-server

# --- Step 6: Verify ---
echo -e "${GREEN}[6/6] Verifying...${NC}"
sleep 2

if systemctl is-active --quiet flac-server; then
    echo -e "${GREEN}  Server is RUNNING!${NC}"
    echo ""
    echo -e "${GREEN}=== Installation Complete ===${NC}"
    echo ""
    echo "  Web UI:    http://$(hostname -I | awk '{print $1}'):8080/web/"
    echo "  API Health: http://$(hostname -I | awk '{print $1}'):8080/api/health"
    echo ""
    echo "  Commands:"
    echo "    systemctl status flac-server    # Check status"
    echo "    systemctl restart flac-server   # Restart"
    echo "    journalctl -u flac-server -f    # View logs"
    echo ""
    echo -e "${GREEN}Ready to use!${NC}"
else
    echo -e "${RED}Server failed to start!${NC}"
    echo -e "${RED}Run: journalctl -u flac-server --no-pager -n 50${NC}"
    exit 1
fi

# --- Step 7: Firewall ---
echo -e "${GREEN}[7/7] Opening firewall...${NC}"
ufw allow 8080/tcp 2>/dev/null || echo "  ufw not available, skipping"

echo ""
echo -e "${GREEN}All done! Open http://YOUR_IP:8080/web/ in browser.${NC}"