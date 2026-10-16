# syntax=docker/dockerfile:1.7
# The bot addon (cmd/mikan-telegram) of this fork. The official bot in the marketplace
# catalog is upstream's image, built from Miroshka000/mikan at the version its entry names
# (tools/telegram of getmikan/marketplace), so a release of this fork publishes its own bot
# here: the release uploads mikan-addon.json beside the installers, and the panel takes the
# bot addon from this repository as an addon source (addons/telegram/README.md).
#
# Build from the repository root (CI passes VERSION and TAG; nothing comes from elsewhere):
#   docker buildx build --platform linux/amd64,linux/arm64 \
#     --build-arg VERSION=0.5.1.3 --build-arg TAG=addon-0.5.1.3 \
#     -f addons/telegram/Dockerfile .
# Go cross-compiles on the build platform; only the final stage is the target's.
FROM --platform=$BUILDPLATFORM golang:1.27@sha256:e0174e51e81218523251d85d248a90d24c3d5e81543b4f07a5d66229397db190 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG VERSION=dev
# Filled in by BuildKit for each platform. A default here would win over it and put
# amd64 binaries into the arm64 image.
ARG TARGETOS TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/mikan-telegram ./cmd/mikan-telegram
# The binary must be the image's own architecture: the ELF machine field says so.
RUN case "$TARGETARCH" in amd64) want="3e 00" ;; arm64) want="b7 00" ;; *) want="" ;; esac; \
    got=$(od -A n -t x1 -j 18 -N 2 /out/mikan-telegram | tr -s " " | sed "s/^ //"); \
    if [ -n "$want" ] && [ "$got" != "$want" ]; then echo "the bot is not built for $TARGETARCH (ELF machine $got)"; exit 1; fi

# CA certificates for the panel and Telegram over TLS, and nothing else: the addon writes
# no files (its state is the panel's KV) and speaks HTTP, so a read-only root is fine. Same
# non-root user as the panel's image (65532), and no shell to keep it small.
FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
ARG TAG=dev
LABEL org.opencontainers.image.title="Arikan Telegram bot addon" \
      org.opencontainers.image.description="The subscribers' bot of the Arikan panel: cmd/mikan-telegram" \
      org.opencontainers.image.version="${TAG}" \
      org.opencontainers.image.source="https://github.com/o0hhs/arikan"
RUN apk add --no-cache ca-certificates && \
    addgroup -S -g 65532 arikan && adduser -S -D -H -u 65532 -G arikan arikan
COPY --from=build /out/mikan-telegram /usr/local/bin/mikan-telegram
# The addon runs on the values the host hands it (MIKAN_PANEL_URL, MIKAN_PANEL_TOKEN,
# MIKAN_BOT_API_URL, MIKAN_BOT_TOKEN): it listens on nothing.
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/mikan-telegram"]
