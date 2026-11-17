#!/bin/sh
# Run the backend and the Vite dev server together with settings from ./.env.
set -eu
cd "$(dirname "$0")/.."

if [ -f .env ]; then
  set -a
  . ./.env
  set +a
fi

# exec the built binary so $backend is the factory itself; killing `go run` leaves its child holding the port
bin="${TMPDIR:-/tmp}/legos-factory-dev"
(cd backend && go build -o "$bin" ./cmd/factory && exec "$bin") &
backend=$!
trap 'kill $backend 2>/dev/null' EXIT INT TERM

cd frontend && npm run dev
