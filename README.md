# GitAI - Activity Monitor

A fast, colorful CLI tool for monitoring GitHub pull requests and GitLab merge requests and issues across repositories. Track your contributions, reviews, and assignments with real-time progress visualization.

fork from [GitAI GitHub feed](https://github.com/zveinn/github-feed)

## Features

- Parallel API fetching for faster scans
- Colorized, grouped output (open/closed/merged)
- Smart MR/issue cross-reference nesting
- Online mode with local BBolt cache
- Offline mode from cache (`--local`)
- Time-window filtering (`--time 1h|2d|3w|4m|1y`)
- Retry/backoff for API rate-limit and transient API errors

## Installation

### Build from source

```bash
go build -o gitlab-feed .
```

### Pre-built binaries

Download from GitHub Releases:

- https://github.com/L0thlorien/gitlab-feed/releases

## Configuration

Select the platform via `--platform github|gitlab` (default: `github`).

Online mode requirements depend on platform:

- GitHub: `GITHUB_TOKEN`, `GITHUB_USERNAME` (and optionally `ALLOWED_REPOS`)
- GitLab: `GITLAB_TOKEN` (or `GITLAB_ACTIVITY_TOKEN`) and `ALLOWED_REPOS`

The app loads configuration in this order:

1. CLI flags
2. Environment variables
3. Platform `.env` file (auto-created on first run)
   - GitHub: `~/.github-feed/.env`
   - GitLab: `~/.gitlab-feed/.env`
4. Built-in defaults

### Environment variables

GitHub:

- `GITHUB_TOKEN` (required online)
- `GITHUB_USERNAME` (required online)
- `ALLOWED_REPOS` (optional; comma-separated `owner/repo`)

GitLab:

- `GITLAB_TOKEN` or `GITLAB_ACTIVITY_TOKEN` (required online)
- `GITLAB_HOST` (optional host override; takes precedence over `GITLAB_BASE_URL`)
- `GITLAB_BASE_URL` (optional base URL, default: `https://gitlab.com`)
- `ALLOWED_REPOS` (required online; comma-separated `group[/subgroup]/repo`)

### Example `.env`

```bash
# GitHub (`--platform github`)
GITHUB_TOKEN=your_token_here
GITHUB_USERNAME=your_username

# Optional in online mode; if omitted, the tool relies on platform defaults/behavior.
ALLOWED_REPOS=owner/repo1,owner/repo2

# GitLab (`--platform gitlab`)
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
# Default: last month (1m), online mode, platform=github
./gitlab-feed

# Explicit platform
./gitlab-feed --platform github
./gitlab-feed --platform gitlab

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
./gitlab-feed --allowed-repos "owner/repo,owner/other"
./gitlab-feed --platform gitlab --allowed-repos "group/repo,group/subgroup/repo"
```

### Flags

| Flag | Description |
|------|-------------|
| `--time RANGE` | Show items from last time range (default: `1m`). Examples: `1h`, `2d`, `3w`, `4m`, `1y` |
| `--platform PLATFORM` | Activity source platform: `github` or `gitlab` (default: `github`) |
| `--debug` | Show detailed API logging |
| `--local` | Use local database instead of API |
| `--links` | Show hyperlinks under each MR/issue |
| `--ll` | Shortcut for `--local --links` |
| `--clean` | Delete and recreate the database cache |
| `--allowed-repos REPOS` | Comma-separated repo paths (GitHub: `owner/repo`; GitLab: `group[/subgroup]/repo`) |

## Data and cache

On first run, the tool creates a platform-specific config dir and cache DB:

- GitHub:
  - `~/.github-feed/.env`
  - `~/.github-feed/github.db`
- GitLab:
  - `~/.gitlab-feed/.env`
  - `~/.gitlab-feed/gitlab.db`

Online mode fetches platform activity and updates cache.
Offline mode (`--local`) reads from cache only.

## Troubleshooting

### GitHub online mode missing token/user

Set `GITHUB_TOKEN` and `GITHUB_USERNAME` in env or `~/.github-feed/.env`.

### GitLab online mode missing token

Set `GITLAB_TOKEN` (or `GITLAB_ACTIVITY_TOKEN`) in env or `~/.gitlab-feed/.env`.

### GitLab online mode missing `ALLOWED_REPOS`

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
