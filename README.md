# boxhand-agent

Remote access to a [tsbox](https://github.com/rif223/tsbox) daemon for Boxhand, similar to the Portainer agent.

The agent runs on the machine of a `tsboxd`, for example a Linux machine with the Firecracker driver. A Boxhand
on another machine adds it as an environment of the kind **Boxhand-Agent** and then manages its boxes, logs,
statistics, events and projects as if they were local.

```
this PC                                   other machine
┌───────────┐   HTTPS, pinned cert   ┌────────────────┐   unix socket   ┌────────┐
│  Boxhand  │ ─────────────────────▶ │ boxhand-agent  │ ──────────────▶ │ tsboxd │
└───────────┘   Bearer token         └────────────────┘                 └────────┘
```

## What it is

- **No UI, no accounts.** One API token, nothing else. Accounts, API tokens, settings, schedules and
  notifications stay with the Boxhand that connects.
- **Only what a remote environment needs:** boxes, logs, stats, events, projects (files, history, up/down,
  check), storage, SDKs, starting a single script, and starting/stopping the daemon. Every other route answers 404.
- **Own TLS certificate.** It is self-signed and created on first use. Boxhand stores its SHA-256 fingerprint
  and only talks to exactly this certificate, so no CA is needed.
- **Token stored as a hash.** The token is shown once by `boxhand-agent init`; the agent keeps only its SHA-256.
- **Projects live on the agent's machine**, where the daemon can read the script files.

## Install

Requirements: Linux with a running `tsboxd` (see `sudo tsbox setup`, optionally `--firecracker`) and Go 1.26
to build.

```bash
make build-linux                       # bin/boxhand-agent-linux-amd64 (and -arm64)
sudo ./scripts/install.sh bin/boxhand-agent-linux-amd64 <user>
```

The install script copies the binary to `/usr/local/bin`, installs the systemd unit
`boxhand-agent.service` for `<user>` (who needs the group `tsbox` to reach the daemon socket) and enables it.
The service keeps retrying until a token exists.

Then, as `<user>`:

```bash
boxhand-agent init --data ~/.config/boxhand-agent
```

It prints the token (only this once) and the certificate fingerprint. Within 10 seconds the service starts
serving on port 9001.

## Add it in Boxhand

Settings → Environments → Add environment → **Boxhand-Agent**:

| Field | Value |
|---|---|
| Address | `https://<agent machine>:9001` |
| Certificate fingerprint | from `boxhand-agent init` or `boxhand-agent fingerprint` |
| Agent token | from `boxhand-agent init` |
| Environment there | `local` |

Only administrators of that Boxhand can use the environment, because the agent token has full rights over the
daemon.

## Network

Open port 9001 only for the machine that runs Boxhand. A new token (`boxhand-agent init` again) replaces the
old one; restart the service afterwards and update the environment in Boxhand.

### Windows with WSL2

The agent can run inside WSL2 (for example Ubuntu with nested virtualization for Firecracker):

- `networkingMode=mirrored` in `%USERPROFILE%\.wslconfig`, so WSL is reachable on the host's LAN address.
- An inbound Hyper-V firewall rule for WSL (in an elevated PowerShell):

  ```powershell
  New-NetFirewallHyperVRule -Name boxhand-agent -DisplayName 'Boxhand-Agent' -Direction Inbound `
    -VMCreatorId '{40E0AC32-46A5-438A-A0B2-2B479E8F2E90}' -Protocol TCP -LocalPorts 9001 `
    -RemoteAddresses <IP of the Boxhand machine> -Action Allow
  ```

- WSL stops a distribution shortly after its last session ends, and with it `tsboxd`, the agent and all boxes.
  A scheduled task at logon that runs `conhost.exe --headless wsl.exe -d Ubuntu-24.04 --exec sleep infinity`
  keeps it running.

## Usage

```
boxhand-agent init          create the certificate and a new token (prints the token once)
boxhand-agent fingerprint   print the certificate fingerprint
boxhand-agent [flags]       serve

  --listen  address (default :9001, always TLS)          BOXHAND_AGENT_LISTEN
  --host    tsboxd address (default unix:///run/tsbox/tsboxd.sock)   TSBOX_HOST
  --tsbox   path to the tsbox CLI (default tsbox)        BOXHAND_TSBOX
  --data    data directory (default ~/.config/boxhand-agent)          BOXHAND_AGENT_DATA
```

The data directory holds `agent.crt`, `agent.key` (mode 0600), `agent.json` (token hash), the project list,
the project history and an activity log of all changing requests (`activity.jsonl`).

## Development

```bash
make test     # go vet + go test
make build    # bin/boxhand-agent for this machine
```

The agent speaks the same HTTP API as Boxhand for one environment, so Boxhand can forward requests to it
unchanged. The handlers under `internal/api` follow Boxhand's; when an endpoint changes there, change it here
too. All routes are listed in `internal/api/routes.go`.

| Package | Purpose |
|---|---|
| `cmd/boxhand-agent` | commands `init`, `fingerprint` and the server |
| `internal/agent` | certificate and token of the agent |
| `internal/api` | HTTP API: token check, routes, handlers, activity log |
| `internal/envs` | the one environment `local` (the tsbox daemon) |
| `internal/projects` | project list and file history |
| `internal/activity`, `internal/cli`, `internal/daemon` | activity log, tsbox CLI, daemon connection |

## License

MIT, see [LICENSE](LICENSE).
