# UDP relay for remote SSH server

Companion binary for `ssh-socks5` UDP support. Must be **built and installed on the remote server** before enabling UDP in the client config.

Listens on `127.0.0.1:<port>` (default `38473`) and proxies framed UDP packets over TCP. Requires a shared **auth key** on connect.

## Build without Go (Docker/Podman)

```bash
./build.sh
# or
GOOS=linux GOARCH=arm64 ./build.sh
```

Uses `golang:1.23-alpine` image. Detects `podman` or `docker` automatically.

## Build with Go

```bash
make build
# binary: ssh-socks5-udp-relay
```

## Install on remote server

```bash
sudo install -m 755 ssh-socks5-udp-relay /usr/local/bin/ssh-socks5-udp-relay
```

## Run manually (testing)

```bash
/usr/local/bin/ssh-socks5-udp-relay -port 38473 -key "your-secret-key"
```

## Client config

```yaml
udp:
  enabled: true
  remote_path: /usr/local/bin/ssh-socks5-udp-relay
  port: 38473
  auth_key: your-secret-key   # must match -key on remote
```

The `auth_key` must be identical on the client and when starting the relay on the server.

See [README.ru.md](../../README.ru.md) / [README.en.md](../../README.en.md) for full setup.
