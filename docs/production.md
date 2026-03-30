# Production Deployment Guide

## Reverse Proxy with TLS

> ⚠️ Never expose Oberwatch without TLS in production.
> API keys pass through the proxy in plain text request headers.
> Always terminate TLS in front of Oberwatch.

### Caddy (recommended — automatic TLS)

Install Caddy, then create `/etc/caddy/Caddyfile`:

```caddy
oberwatch.yourdomain.com {
    reverse_proxy localhost:8080
}
```

Caddy automatically provisions and renews Let's Encrypt certificates.
Start with: `sudo systemctl enable --now caddy`

### Nginx

```nginx
server {
    listen 443 ssl http2;
    server_name oberwatch.yourdomain.com;

    ssl_certificate /etc/letsencrypt/live/oberwatch.yourdomain.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/oberwatch.yourdomain.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        # Required for SSE (Server-Sent Events)
        proxy_buffering off;
        proxy_cache off;
        proxy_read_timeout 86400s;
        proxy_http_version 1.1;
        proxy_set_header Connection '';
    }
}

server {
    listen 80;
    server_name oberwatch.yourdomain.com;
    return 301 https://$host$request_uri;
}
```

## Firewall

Only expose port 443 publicly. Keep port 8080 on localhost only.

```bash
sudo ufw allow 443/tcp
sudo ufw allow 22/tcp
sudo ufw deny 8080/tcp
sudo ufw enable
```

## Persistent Storage

### Binary install
Data lives in `~/.oberwatch/data/oberwatch.db` by default.

### Docker
Always use a named volume. Without `-v`, data is lost on container removal:

```bash
docker run -d -p 8080:8080 -v oberwatch-data:/data ghcr.io/oberwatch/oberwatch:latest
```

## Backups

SQLite supports hot backups — copy the file while Oberwatch is running:

```bash
# Binary install
cp ~/.oberwatch/data/oberwatch.db ~/backups/oberwatch-$(date +%Y%m%d).db

# Docker
docker cp oberwatch:/data/oberwatch.db ~/backups/oberwatch-$(date +%Y%m%d).db
```

Automate with a daily cron job.

## Upgrading

### Binary
Re-run the install script. It upgrades the binary without touching config or data:

```bash
curl -fsSL https://raw.githubusercontent.com/OberWatch/oberwatch/main/scripts/install.sh | sh
```

### Docker

```bash
docker pull ghcr.io/oberwatch/oberwatch:latest
docker stop oberwatch && docker rm oberwatch
docker run -d -p 8080:8080 -v oberwatch-data:/data --name oberwatch ghcr.io/oberwatch/oberwatch:latest
```

## Resource Requirements

Oberwatch is lightweight:
- RAM: ~30-50MB baseline
- CPU: minimal — it proxies requests, it doesn't run inference
- Disk: ~500MB-1GB for a busy deployment (100K requests/day, 7-day retention)

## Health Monitoring

Poll the health endpoint from your uptime monitor:

```bash
curl http://localhost:8080/_oberwatch/api/v1/health
```

Returns HTTP 200 with status `"ok"` when healthy. Includes `emergency_stop` state and provider reachability.
