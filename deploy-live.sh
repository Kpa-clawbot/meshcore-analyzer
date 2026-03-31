#!/bin/bash
set -e

DEPLOY_DIR="$(cd "$(dirname "$0")" && pwd)"
MATOMO_COMMIT="bb8fb43"

cd "$DEPLOY_DIR"

echo "[deploy] Fetching latest from origin..."
git fetch origin

echo "[deploy] Resetting to origin/master..."
git reset --hard origin/master

echo "[deploy] Building Docker image..."
docker build -t meshcore-analyzer .

echo "[deploy] Stopping old container (30s grace period)..."
docker stop -t 30 meshcore-analyzer && docker rm meshcore-analyzer
docker run -d --name meshcore-analyzer \
  --restart unless-stopped \
  -p 3000:3000 \
  -v "$(pwd)/config.json:/app/config.json:ro" \
  -v meshcore-data:/app/data \
  meshcore-analyzer

echo "[deploy] Done. Live at https://analyzer.on8ar.eu"
