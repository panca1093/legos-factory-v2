FROM node:22-bookworm-slim

ARG GO_VERSION=1.26.3
ARG TARGETARCH

RUN apt-get update \
 && apt-get install -y --no-install-recommends git ca-certificates curl make build-essential openssh-client \
 && rm -rf /var/lib/apt/lists/*

RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${TARGETARCH}.tar.gz" | tar -C /usr/local -xz

# codex-acp bundles its @openai/codex binary (linux x64/arm64 optional deps); @zed-industries/codex-acp is deprecated
# TODO: uncertain — `opencode acp` was exercised on the 2.1.3 binary only; npm bin of @opencode/cli@2.2.2 is `opencode`, verify ACP on first image build
RUN npm i -g @agentclientprotocol/claude-agent-acp@0.99.1 @opencode/cli@2.2.2 @agentclientprotocol/codex-acp@2.5.0

ENV PATH=/usr/local/go/bin:$PATH \
    HOME=/ws/home \
    GOCACHE=/cache/go \
    GOMODCACHE=/cache/gomod \
    npm_config_cache=/cache/npm

# Pre-created and owned so fresh named volumes inherit usable ownership.
RUN useradd -m -u 1001 -d /ws/home factory \
 && mkdir -p /ws /cache/go /cache/gomod /cache/npm \
 && chown -R factory:factory /ws /cache

USER factory
WORKDIR /ws
CMD ["sleep", "infinity"]
