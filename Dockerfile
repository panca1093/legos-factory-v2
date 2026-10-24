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
WORKDIR /app
COPY --from=backend /factory /usr/local/bin/factory
ENV FACTORY_ADDR=0.0.0.0:8080 \
    FACTORY_DATA_DIR=/data
EXPOSE 8080
ENTRYPOINT ["factory"]
