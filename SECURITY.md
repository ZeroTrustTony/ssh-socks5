# Security Policy

## Reporting a Vulnerability

We take the security of ssh-socks5 seriously. If you believe you have found a security vulnerability, please report it responsibly.

**Please do not report security vulnerabilities through public GitHub issues.**

Instead, use the **Private vulnerability reporting** feature:
1. Navigate to the repository's **Security** tab.
2. Click **"Report a vulnerability"**.
3. Provide a detailed description of the issue, including steps to reproduce and potential impact.

You should receive an initial response within **48 hours**. If the issue is confirmed, we will work on a patch and coordinate the disclosure.

## Security Best Practices for Users

When deploying `ssh-socks5`, follow these recommendations to ensure a secure configuration:

### 1. Secure Your Configuration File
The `config.yaml` file contains sensitive credentials (SSH keys, passwords, SOCKS5 credentials, UDP auth keys).
- **Set strict permissions:** Restrict access to the file owner only:
  ```bash
  chmod 600 config.yaml
  ```
- **Mount as read-only:** When using the Docker/Podman container, mount the config file with the `:ro` flag to prevent accidental modification.

### 2. Enable SSH Host Key Verification
By default, SSH host key verification is **disabled** for convenience. This leaves the tunnel vulnerable to man-in-the-middle attacks.
- **For production use, you must enable and pin the server's public key.**
- Obtain the server's public key (e.g., `ssh-keyscan -t ed25519 <your-server>`).
- Set the following in your `config.yaml`:
  ```yaml
  ssh:
    host_key:
      verify: true
      key: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA..." # Paste the full public key string
  ```

### 3. Protect Credentials
- **Avoid storing credentials in plaintext** where possible. Use environment variables or secret management tools.
- **Use SSH key-based authentication** over passwords for a more secure and automated setup.
- **Use strong, unique passwords** for SOCKS5 authentication if enabled.

### 4. Network Security
- **Do not expose the SOCKS5 proxy port** (default `1080`) to the public internet without proper authentication and firewall rules.
- **Bind the SOCKS5 listener** to `127.0.0.1` (or a specific internal interface) if it should only be accessed locally. Change the `socks5.listen` address in your configuration.
- **Restrict the remote SSH user** on the server using `sshd_config` rules (e.g., `AllowTcpForwarding yes`, `PasswordAuthentication no`).

### 5. Operational Security
- **Enable the startup test** to verify the tunnel is functioning and the configuration is correct.
- **Monitor logs** for unusual activity. Set an appropriate `log_level` (e.g., `info` or `error`).
- **Keep the container and dependencies updated** by pulling the latest image (`ghcr.io/zerotrusttony/ssh-socks5:latest`) regularly.

## Security Features in the Project

- **On-demand Tunnel:** The SSH tunnel is only established when a client connects, reducing the attack surface.
- **SOCKS5 Authentication:** Supports username/password authentication for the proxy.
- **SSH Host Key Pinning:** Offers protection against MITM attacks when enabled.
- **UDP Relay Authentication:** Uses a shared key to authenticate requests to the UDP relay, preventing unauthorized use by other local processes on the server.
- **Read-Only Config Mounts:** Encouraged in Docker/Podman deployments to prevent runtime tampering.
- **Health Check:** Monitors the service health without exposing internal details.
