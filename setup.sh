#!/bin/bash
# Run this on your Ubuntu server to deploy the Go API

set -e

# ── 1. Install Go (skip if already installed) ─────────────────────────────────
if ! command -v go &> /dev/null; then
  wget https://go.dev/dl/go1.22.3.linux-amd64.tar.gz
  sudo tar -C /usr/local -xzf go1.22.3.linux-amd64.tar.gz
  echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
  source ~/.bashrc
fi

# ── 2. Copy project to server ─────────────────────────────────────────────────
sudo mkdir -p /var/www/goapi
sudo cp -r ./* /var/www/goapi/
sudo chown -R www-data:www-data /var/www/goapi

# ── 3. Install dependencies & build ───────────────────────────────────────────
cd /var/www/goapi
sudo -u www-data /usr/local/go/bin/go mod tidy
sudo -u www-data /usr/local/go/bin/go build -o goapi .

# ── 4. Enable Apache2 proxy modules ───────────────────────────────────────────
sudo a2enmod proxy proxy_http
sudo cp apache-goapi.conf /etc/apache2/sites-available/goapi.conf
sudo a2ensite goapi.conf
sudo systemctl reload apache2

# ── 5. Install and start systemd service ──────────────────────────────────────
# Update ExecStart in goapi.service to use compiled binary:
sed -i 's|ExecStart=.*|ExecStart=/var/www/goapi/goapi|' goapi.service
sudo cp goapi.service /etc/systemd/system/goapi.service
sudo systemctl daemon-reload
sudo systemctl enable goapi
sudo systemctl start goapi

echo ""
echo "✅ Done! API running at http://api.yourdomain.com"
echo "Check status: sudo systemctl status goapi"
echo "View logs:    sudo journalctl -u goapi -f"
