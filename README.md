# GitAI - GitLab Activity Monitor

GitLab Feed is a Go CLI for monitoring GitLab merge requests and issues across a bounded set of projects.

fork from [GitAI GitHub feed](https://github.com/zveinn/github-feed)

## Features

- Parallel API fetching for faster scans
- Colorized, grouped output (open/closed/merged)
- Smart MR/issue cross-reference nesting
- Online mode with local BBolt cache
- Offline mode from cache (`--local`)
- Time-window filtering (`--time 1h|2d|3w|4m|1y`)
- Retry/backoff for GitLab rate-limit and transient API errors

## Installation

### Build from source

```bash
go build -o gitlab-feed .
```

### Pre-built binaries

Download from GitHub Releases:

- https://github.com/zveinn/github-feed/releases

## Configuration

Online mode requires:

- `GITLAB_TOKEN` (or `GITLAB_ACTIVITY_TOKEN`)
- `ALLOWED_REPOS`

The app loads configuration in this order:

1. Environment variables
2. `~/.gitlab-feed/.env` (auto-created on first run)

Environment variables take precedence over `.env` values.

### Environment variables

- `GITLAB_TOKEN` or `GITLAB_ACTIVITY_TOKEN` (required online)
- `GITLAB_HOST` (optional host override; takes precedence over `GITLAB_BASE_URL`)
- `GITLAB_BASE_URL` (optional base URL, default: `https://gitlab.com`)
- `ALLOWED_REPOS` (required online; comma-separated `group[/subgroup]/repo`)

### Example `.env`

```bash
GITLAB_TOKEN=your_token_here

# Optional host override, e.g. self-managed GitLab
# If set, this overrides GITLAB_BASE_URL.
GITLAB_HOST=http://1.1.1.1

# Optional explicit base URL (defaults to https://gitlab.com)
GITLAB_BASE_URL=https://gitlab.com

# Required in online mode
ALLOWED_REPOS=team/repo1,platform/backend/repo2
```

### Token scopes

Create a GitLab Personal Access Token with:

- `read_api` (recommended)
- `api` only if your self-managed setup requires broader scope

Reference:

- https://docs.gitlab.com/user/profile/personal_access_tokens/

## Usage

```bash
# Default: last month (1m), online mode
./gitlab-feed

# Time window examples
./gitlab-feed --time 3h
./gitlab-feed --time 2d
./gitlab-feed --time 3w
./gitlab-feed --time 6m
./gitlab-feed --time 1y

# Debug output
./gitlab-feed --debug

# Offline from cache
./gitlab-feed --local

# Show links
./gitlab-feed --links

# Shortcut: --local --links
./gitlab-feed --ll

# Recreate cache DB
./gitlab-feed --clean

# Override allowed repos from CLI
./gitlab-feed --allowed-repos "group/repo,group/subgroup/repo"
```

### Flags

| Flag | Description |
|------|-------------|
| `--time RANGE` | Show items from last time range (default: `1m`). Examples: `1h`, `2d`, `3w`, `4m`, `1y` |
| `--debug` | Show detailed API logging |
| `--local` | Use local database instead of GitLab API |
| `--links` | Show hyperlinks under each MR/issue |
| `--ll` | Shortcut for `--local --links` |
| `--clean` | Delete and recreate the database cache |
| `--allowed-repos REPOS` | Comma-separated project paths (`group/repo,group/subgroup/repo`) |

## Data and cache

On first run, the tool creates:

- `~/.gitlab-feed/.env`
- `~/.gitlab-feed/gitlab.db`

Online mode fetches GitLab activity and updates cache.
Offline mode (`--local`) reads from cache only.

## Troubleshooting

### `token is required for GitLab API mode`

Set `GITLAB_TOKEN` (or `GITLAB_ACTIVITY_TOKEN`) in env or `~/.gitlab-feed/.env`.

### `ALLOWED_REPOS is required for GitLab API mode`

Set `ALLOWED_REPOS` with valid project paths (`group[/subgroup]/repo`).

### No open activity found

Try:

- `--debug` to inspect resolved repos and API base URL
- a wider window (for example `--time 24h`)
- verifying `ALLOWED_REPOS` matches exact project paths

## Development

```bash
go test ./... -count=1
go build -o gitlab-feed .
```

Current core files:

- `main.go`
- `db.go`
- `priority_test.go`
- `CLAUDE.md`
- `README.md`

## License

MIT
