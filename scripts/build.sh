#!/bin/sh
# Build the production single-binary legos-factory:
# frontend dist is embedded into the Go binary with SPA fallback.
set -eu
cd "$(dirname "$0")/.."

(cd frontend && npm run build)

rm -rf backend/internal/web/dist
cp -R frontend/dist backend/internal/web/dist

(cd backend && go build -tags embedstatic -o ../factory-server ./cmd/factory)
echo "built ./factory-server (frontend embedded)"
