#!/bin/sh
# Run the backend and the Vite dev server together with settings from ./.env.
set -eu
cd "$(dirname "$0")/.."

if [ -f .env ]; then
  set -a
  . ./.env
  set +a
fi

(cd backend && go run ./cmd/factory) &
backend=$!
trap 'kill $backend 2>/dev/null' EXIT INT TERM

cd frontend && npm run dev
