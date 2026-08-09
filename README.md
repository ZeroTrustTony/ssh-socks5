# ssh-socks5

On-demand SOCKS5 proxy over an SSH tunnel.

The service establishes an SSH connection to a remote server **only when a client connects** to the local SOCKS5 proxy. When there are no active connections for the configured `idle_timeout` period, the tunnel is closed automatically.

## Features

- SOCKS5 TCP (always) and UDP ASSOCIATE (optional)
- IPv4 and IPv6
- SOCKS5 authentication (username/password)
- SSH authentication by key or password
- Optional SSH host key verification (pinned public key)
- On-demand SSH tunnel with idle timeout
- Automatic SSH reconnection on disconnect
- UDP relay with shared-key authentication
- Optional tunnel test at startup (curl)
- Docker/Podman image with health check and curl
- Low footprint: ~6–7 MB RAM at runtime

## Quick start

### Build

```bash
make build
```

### Run

```bash
cp config.example.yaml config.yaml
# edit config.yaml
./ssh-socks5 -config config.yaml
```

UDP is **disabled** by default — sufficient for `curl`, HTTPS, and most applications.

### Build and run with Podman

```bash
podman machine start   # on macOS, if the machine is not running yet
podman build -t ssh-socks5 .
podman run -d \
  --name ssh-socks5 \
  -v ./config.yaml:/etc/ssh-socks5/config.yaml:ro \
  -v ./id_ed25519:/etc/ssh-socks5/id_ed25519:ro \
  -p 1080:1080 \
  ssh-socks5
```

Check health status:

```bash
podman inspect --format '{{.State.Health.Status}}' ssh-socks5
```

## Configuration

See `config.example.yaml` for a full example:

| Parameter | Description |
|---|---|
| `ssh.host`, `ssh.port`, `ssh.user` | SSH server address and username |
| `ssh.auth.method` | `key` or `password` |
| `ssh.auth.private_key_path` | Path to private key (for `key`) |
| `ssh.auth.password` | SSH password (for `password`) |
| `ssh.host_key.verify` | Verify the SSH server host key (`true`/`false`, default `false`) |
| `ssh.host_key.key` | Pinned server public key (required when `verify: true`) |
| `socks5.listen` | Local SOCKS5 proxy listen address |
| `socks5.username`, `socks5.password` | SOCKS5 credentials |
| `udp.enabled` | Enable UDP ASSOCIATE (`true`/`false`) |
| `udp.remote_path` | Path to UDP relay on remote (when `enabled: true`) |
| `udp.port` | UDP relay TCP port on remote `127.0.0.1` (default `38473`) |
| `udp.auth_key` | Shared UDP relay auth key (when `enabled: true`) |
| `startup_test.enabled` | Run tunnel test at startup |
| `startup_test.url` | Test URL (default `https://www.google.com`) |
| `idle_timeout` | Idle period before SSH is closed |
| `max_clients` | Concurrent client limit (`0` = unlimited) |
| `log_level` | `debug`, `info`, or `error` |

### UDP disabled (default)

```yaml
udp:
  enabled: false
```

### UDP enabled

```yaml
udp:
  enabled: true
  remote_path: /usr/local/bin/ssh-socks5-udp-relay
  port: 38473
  auth_key: your-long-random-secret
```

### Startup tunnel test

```yaml
startup_test:
  enabled: true
  url: https://www.google.com
```

On startup the service establishes the SSH tunnel, runs `curl` through SOCKS5, and logs the result:

```
INFO  startup test: OK (HTTP 200, 15.2 KB transferred, 42.1 KB/s, 0.36 s, https://www.google.com)
INFO  startup test: FAILED (HTTP 403, 512 B transferred, 1.2 KB/s, 0.41 s, https://www.google.com)
INFO  startup test: FAILED (tunnel: ...)
```

Requires `curl` in PATH (included in the Docker image).

### Config file permissions

The config file contains secrets (SSH password/key path, SOCKS5 credentials, UDP
auth key). Restrict access to the owner only:

```bash
chmod 600 config.yaml
```

When mounting into Docker/Podman, keep it read-only (`:ro`) as shown above.

### SSH host key verification

By default host key verification is **disabled** (`ssh.host_key.verify: false`),
which leaves the tunnel vulnerable to man-in-the-middle attacks. This is
convenient for a container image that has no `known_hosts`, but for real use you
should pin the server's public key.

Get the server's public key on the server:

```bash
cat /etc/ssh/ssh_host_ed25519_key.pub
```

or fetch it from a client:

```bash
ssh-keyscan -t ed25519 example.com
```

Then enable verification in `config.yaml` (paste the key without the leading
hostname):

```yaml
ssh:
  host_key:
    verify: true
    key: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA..."
```

On connect the server's key is compared against the pinned value; a mismatch
aborts the tunnel. This lets a containerized instance authenticate the SSH
server without a `known_hosts` file.

## UDP relay on the remote server

UDP support is **not embedded** in the main client. The binary from `distrib/udp-relay/` must be **installed on the SSH server** beforehand.

### Build without Go (Docker/Podman)

```bash
cd distrib/udp-relay
./build.sh
# arm64:
GOOS=linux GOARCH=arm64 ./build.sh
```

Or from project root:

```bash
make build-udp-relay-docker
```

### Build with Go

```bash
cd distrib/udp-relay
make build
```

### Install on server

```bash
sudo install -m 755 ssh-socks5-udp-relay /usr/local/bin/ssh-socks5-udp-relay
```

The client config (`udp.auth_key`, `udp.port`) must match the relay parameters. The client starts the relay automatically:

```
/usr/local/bin/ssh-socks5-udp-relay -port 38473 -key "your-secret"
```

See [distrib/udp-relay/README.md](distrib/udp-relay/README.md) for details.

### UDP relay authentication

On TCP connect to `127.0.0.1:<port>`, the client sends a shared key. Without the correct key, the relay closes the connection. This protects against other local processes on the server using the relay.

## Remote SSH user setup

**TCP:** `AllowTcpForwarding yes`

**UDP:** binary execution via SSH exec (requires a working shell, not `nologin`)

```bash
# TCP only
useradd -m -s /usr/sbin/nologin socks5tunnel

# TCP + UDP
useradd -m -s /bin/sh socks5tunnel
```

Restrictions in `/etc/ssh/sshd_config.d/socks5tunnel.conf`:

```sshconfig
Match User socks5tunnel
    AllowTcpForwarding yes
    PermitTTY no
    PasswordAuthentication no
    AuthenticationMethods publickey
```

## Using the SOCKS5 proxy

```bash
curl --socks5 proxyuser:proxypass@127.0.0.1:1080 https://ifconfig.me
```

## Service behavior

1. Start → SOCKS5 listens → (optional) startup test
2. No clients → no SSH connection
3. Client connects → SSH tunnel is established
4. When UDP enabled → relay starts on remote with auth key
5. Idle timeout → tunnel is closed

## Health check

```bash
./ssh-socks5 -health-check -config config.yaml
```

Enabled automatically in Docker/Podman images. The container health check runs
every 5 minutes. A longer interval is used deliberately: each check writes the
result to the container state, so a short interval generates constant disk I/O
even when the proxy is idle. If you prefer faster health feedback, lower
`--interval` in the `Dockerfile` at the cost of more block writes.

## Resource usage

- **Memory:** the running container uses about **6–7 MB RAM**.
- **Disk I/O:** the service does not write to disk during normal operation; it
  only logs to stdout/stderr (captured by the container runtime). The main
  source of periodic block writes is the Docker health check, which is why its
  interval is set to 5 minutes (see above). To further reduce writes, cap the
  runtime log driver, e.g. `--log-opt max-size=1m --log-opt max-file=3`, or set
  `log_level: error` to reduce log volume.

## Building

```bash
make build                   # main client
make build-udp-relay         # UDP relay (requires Go)
make build-udp-relay-docker  # UDP relay via Docker/Podman
```

## Logging (INFO)

- `ssh-socks5 listening on ...` — service start
- `establishing SSH tunnel` / `SSH tunnel established` — tunnel up
- `idle timeout reached, closing SSH tunnel` / `closing SSH tunnel` — tunnel down (with uptime and transferred data)
- `startup test: OK/FAILED` — startup test result

Everything else (TCP CONNECT, UDP, idle timer) is logged at `debug` level.
