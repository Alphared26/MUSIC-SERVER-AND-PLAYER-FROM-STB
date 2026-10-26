#!/bin/bash
# ============================================
# FLAC Server — Full Reset Script for HG680P
# Use this if you want to reinstall from scratch
# ============================================

set -e

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'

echo -e "${GREEN}=== FLAC Server Install (Full) ===${NC}"

# Step 1: Install Go
echo -e "${GREEN}[1/7] Installing Go...${NC}"
curl -sL https://go.dev/dl/go1.23.4.linux-arm64.tar.gz | tar -C /usr/local -xz
export PATH=$PATH:/usr/local/go/bin
echo 'export PATH=$PATH:/usr/local/go/bin' >> /etc/profile.d/go.sh
go version

# Step 2: Create directories
echo -e "${GREEN}[2/7] Creating directories...${NC}"
mkdir -p /mnt/hdd/music
mkdir -p /mnt/hdd/music_writable
mkdir -p /DATA/AppData/flac-server
mkdir -p /DATA/AppData/flac-server/art
mkdir -p /DATA/AppData/flac-server/tmp

# Step 3: Download project (replace URL)
echo -e "${GREEN}[3/7] Downloading project...${NC}"
cd /root
git clone https://github.com/username/flac-server.git
cd flac-server

# Step 4: Build
echo -e "${GREEN}[4/7] Building...${NC}"
export PATH=$PATH:/usr/local/go/bin
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o /usr/local/bin/flac-server ./cmd/server/

# Step 5: Systemd
echo -e "${GREEN}[5/7] Setting up service...${NC}"
cat > /etc/systemd/system/flac-server.service << 'EOF'
[Unit]
Description=FLAC Streaming Server
After=network.target sound.target

[Service]
Type=simple
ExecStart=/usr/local/bin/flac-server
Restart=always
RestartSec=5
WorkingDirectory=/root/flac-server
Environment=CONFIG_PATH=/root/flac-server/config.yaml
Environment=GOMEMLIMIT=80MiB
Environment=GOGC=50
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable flac-server
systemctl start flac-server

# Step 6: Verify
echo -e "${GREEN}[6/7] Verifying...${NC}"
sleep 2
curl -s http://localhost:8080/api/health && echo ""
systemctl is-active --quiet flac-server && echo -e "${GREEN}Server running!${NC}" || echo -e "${RED}Server failed!${RED}"

# Step 7: Firewall
echo -e "${GREEN}[7/7] Firewall...${NC}"
ufw allow 8080/tcp 2>/dev/null || true

echo -e "${GREEN}=== Done! ===${NC}"
echo "Access: http://$(hostname -I | awk '{print $1}'):8080/web/"