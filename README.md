# anssh

Simple SSH CLI for Ansible inventory (`hosts.yml`).

`anssh` reads host settings from inventory (via `ansible-inventory --list`) and runs:

- SSH connect
- Local tunnel with auto-reconnect
- Remote command execution
- Host listing

## Installation

```bash
go install github.com/akhmanov/anssh/cmd/anssh@latest
```

## Requirements

- Go 1.25+
- `ssh` available in `PATH`
- `ansible-inventory` available in `PATH`
- Ansible-style inventory YAML (`hosts.yml`)

## Inventory source

Set inventory via:

- `-i, --inventory <path>`
- `ANSSH_INVENTORY` env var

If both are set, `--inventory` is used.

Optional command override:

- `ANSSH_ANSIBLE_INVENTORY_CMD` (default: `ansible-inventory`)

Examples:

```bash
ANSSH_ANSIBLE_INVENTORY_CMD="ansible-inventory --playbook-dir /path/to/repo" anssh -i ./inventory/hosts.yml list
ANSSH_ANSIBLE_INVENTORY_CMD="/opt/homebrew/bin/ansible-inventory" anssh doctor
```

## Usage

```bash
anssh [global options] <target> [ssh args...]
anssh [global options] tunnel <target> <local:remote>
anssh [global options] exec <target> <command>
anssh [global options] list
anssh [global options] doctor
```

### Global options

```bash
-i, --inventory <path>   Path to hosts.yml
```

## Examples

```bash
# Connect (default action)
anssh -i ./inventory/hosts.yml prod-app-1

# Connect with extra ssh args
anssh -i ./inventory/hosts.yml prod-app-1 -- -v

# Tunnel localhost:5432 -> remote localhost:5432
anssh -i ./inventory/hosts.yml tunnel prod-pg 54320:5432

# Execute remote command
anssh -i ./inventory/hosts.yml exec prod-app-1 "docker ps"

# List hosts from inventory
anssh -i ./inventory/hosts.yml list

# Check required binaries in PATH
anssh doctor
```

## Supported host fields

`anssh` uses these inventory variables:

- `ansible_host`
- `ansible_user`
- `ansible_ssh_private_key_file`
- `ansible_ssh_common_args`

## Tunnel behavior

`tunnel` uses SSH local forwarding (`-L`) with `-N` and auto-reconnect:

- Backoff: `1s -> 2s -> 4s -> ... -> 30s (max)`
- Stop with `Ctrl+C`
