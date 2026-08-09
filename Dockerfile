# Build stage — runs on the native build host and cross-compiles to the target arch.
FROM --platform=$BUILDPLATFORM golang:1.26.5-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# TARGETOS/TARGETARCH are provided automatically by Docker Buildx.
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -ldflags="-s -w" -o ssh-socks5 ./cmd/ssh-socks5

# Runtime stage
FROM alpine:3.23.5

RUN apk add --no-cache ca-certificates tzdata curl && \
    addgroup -S sshsocks5 && adduser -S sshsocks5 -G sshsocks5

ENV TZ=Europe/Moscow

COPY --from=builder /build/ssh-socks5 /usr/local/bin/ssh-socks5
COPY config.example.yaml /etc/ssh-socks5/config.yaml

USER sshsocks5

EXPOSE 1080

# Longer interval keeps container-state disk writes low for an idle on-demand proxy.
HEALTHCHECK --interval=5m --timeout=10s --start-period=10s --retries=3 \
  CMD ["/usr/local/bin/ssh-socks5", "-health-check", "-config", "/etc/ssh-socks5/config.yaml"]

ENTRYPOINT ["/usr/local/bin/ssh-socks5"]
CMD ["-config", "/etc/ssh-socks5/config.yaml"]
