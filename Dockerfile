FROM node:22-bookworm-slim AS frontend
WORKDIR /frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

FROM golang:1.26.3-bookworm AS backend
WORKDIR /backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
COPY --from=frontend /frontend/dist ./internal/http/dist
RUN CGO_ENABLED=0 go build -tags embedstatic -o /factory ./cmd/factory

FROM golang:1.26.3-bookworm
# Work-order checks need Go, Node/npm, and Git at runtime.
COPY --from=frontend /usr/local/ /usr/local/
# Agents run as `opencode acp` subprocesses (FACTORY_HARNESS=acp); v2 ships as @opencode/cli.
ARG OPENCODE_VERSION=2.1.3
RUN npm install -g @opencode/cli@${OPENCODE_VERSION} && npm cache clean --force
WORKDIR /app
COPY --from=backend /factory /usr/local/bin/factory
ENV FACTORY_ADDR=0.0.0.0:8080 \
    FACTORY_DATA_DIR=/data \
    OPENCODE_DISABLE_AUTOUPDATE=1
EXPOSE 8080
ENTRYPOINT ["factory"]
