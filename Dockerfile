# Build stages run natively on the build host ($BUILDPLATFORM) and
# cross-compile for the target — the arm64 image never pays the QEMU
# emulation tax for tsc or the Go compiler. Only the tiny runtime stage
# executes under emulation.

# ---- frontend ----
FROM --platform=$BUILDPLATFORM node:24-alpine AS webbuild
WORKDIR /src/web
RUN corepack enable
# pnpm-workspace.yaml must ride along: it carries the supply-chain policy
# (minimumReleaseAge + exclusions) — without it pnpm falls back to defaults
# and rejects daily-release packages like electron-to-chromium
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

# ---- backend ----
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS gobuild
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=webbuild /src/web/dist ./web/dist
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /booky ./cmd/booky

# ---- runtime ----
FROM alpine:3.23
RUN apk add --no-cache su-exec tzdata ca-certificates
COPY --from=gobuild /booky /usr/local/bin/booky
COPY docker/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

ENV PUID=99 PGID=100 UMASK=022 BOOKY_PORT=8787 BOOKY_CONFIG_DIR=/config
EXPOSE 8787
VOLUME ["/config", "/data"]
ENTRYPOINT ["/entrypoint.sh"]
