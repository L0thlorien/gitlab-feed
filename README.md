# GitAI - Activity Monitor

A fast, colorful CLI tool for monitoring pull requests and issues across repositories. Track your contributions, reviews, and assignments with real-time progress visualization.

## Features

- **Parallel API Calls** - Fetches data concurrently for maximum speed
- **Colorized Output** - Easy-to-read color-coded labels, states, and progress
- **Smart Cross-Referencing** - Automatically links related PRs and issues
- **Real-Time Progress Bar** - Visual feedback with color-coded completion status
- **Comprehensive Search** - Tracks authored, mentioned, assigned, commented, and reviewed items
- **Time Filtering** - View items from the last month by default (configurable with `--time`)
- **Organized Display** - Separates open, merged, and closed items into clear sections

## Installation

### Pre-built Binaries (Recommended)

Download the latest release for your platform from the [releases page](https://github.com/zveinn/github-feed/releases):

**macOS**
```bash
# Intel Mac
curl -L https://github.com/zveinn/github-feed/releases/latest/download/gitlab-feed_<VERSION>_Darwin_x86_64.tar.gz | tar xz
chmod +x gitlab-feed
sudo mv gitlab-feed /usr/local/bin/

# Apple Silicon Mac
curl -L https://github.com/zveinn/github-feed/releases/latest/download/gitlab-feed_<VERSION>_Darwin_arm64.tar.gz | tar xz
chmod +x gitlab-feed
sudo mv gitlab-feed /usr/local/bin/
```

**Linux**
```bash
# x86_64
curl -L https://github.com/zveinn/github-feed/releases/latest/download/gitlab-feed_<VERSION>_Linux_x86_64.tar.gz | tar xz
chmod +x gitlab-feed
sudo mv gitlab-feed /usr/local/bin/

# ARM64
curl -L https://github.com/zveinn/github-feed/releases/latest/download/gitlab-feed_<VERSION>_Linux_arm64.tar.gz | tar xz
chmod +x gitlab-feed
sudo mv gitlab-feed /usr/local/bin/


```

**Windows**

Download the appropriate `.zip` file from the releases page, extract it, and add `gitlab-feed.exe` to your PATH.

### Build from Source

```bash
go build -o gitlab-feed .
```

### Release Management

Releases are automatically built and published via GitHub Actions using GoReleaser:

```bash
# Create a new release
git tag -a v1.0.0 -m "Release v1.0.0"
git push origin v1.0.0
```

This will automatically:
- Build binaries for Linux (amd64, arm64), macOS (Intel, Apple Silicon), and Windows (amd64)
- Generate checksums for all releases
- Create a GitHub release with installation instructions
- Publish all artifacts to the releases page

## Configuration

### First Run Setup

On first run, GitAI automatically creates a configuration directory at `~/.gitlab-feed/` with:
- `.env` - Configuration file (with helpful template)
- `gitlab.db` - Local database for caching activity data

### API Token Setup

Create a GitLab Personal Access Token with:
- `read_api` (recommended)
- `api` (use this only if your self-managed instance requires it)

GitLab.com token page: https://gitlab.com/-/user_settings/personal_access_tokens

### Environment Setup

You can provide your token and optional username in two ways:

**Option 1: Configuration File (Recommended)**

Edit `~/.gitlab-feed/.env` and add your credentials:
```bash
# Your GitLab Personal Access Token (required for online mode)
GITLAB_TOKEN=your_token_here

# Optional fallback token variable
# GITLAB_ACTIVITY_TOKEN=your_token_here

# Optional username (the tool can also resolve current user via API)
GITLAB_USERNAME=your_username

# Optional for self-managed GitLab (defaults to https://gitlab.com)
# Example: http://10.10.1.207/
GITLAB_BASE_URL=https://gitlab.com

# Required in online mode: comma-separated project paths
# Format: group[/subgroup]/repo
ALLOWED_REPOS=team/repo1,platform/backend/repo2
```

**Option 2: Environment Variables**
```bash
export GITLAB_TOKEN="your_token_here"
export GITLAB_USERNAME="your_username"  # Optional
export GITLAB_BASE_URL="https://gitlab.com"  # Optional
export ALLOWED_REPOS="team/repo1,platform/backend/repo2"  # Required in online mode
```

**Note:** Environment variables take precedence over the `.env` file.

If both are set, `GITLAB_HOST` takes precedence over `GITLAB_BASE_URL`.

## Usage

### Basic Usage

```bash
# Monitor merge requests and issues from the last month (default, fetches from API)
gitlab-feed

# Show items from the last 3 hours
gitlab-feed --time 3h

# Show items from the last 2 days
gitlab-feed --time 2d

# Show items from the last 3 weeks
gitlab-feed --time 3w

# Show items from the last 6 months
gitlab-feed --time 6m

# Show items from the last year
gitlab-feed --time 1y

# Show detailed logging output
gitlab-feed --debug

# Use local database instead of API (offline mode)
gitlab-feed --local

# Show hyperlinks underneath each merge request/issue
gitlab-feed --links

# Delete and recreate the database cache (start fresh)
gitlab-feed --clean

# Filter to specific repositories only
gitlab-feed --allowed-repos="team/repo1,platform/backend/repo2"

# Quick offline mode with links (combines --local and --links)
gitlab-feed --ll

# Combine flags
gitlab-feed --local --time 2w --debug --links --allowed-repos="team/repo1,platform/backend/repo2"
```

### Command Line Options

| Flag | Description |
|------|-------------|
| `--time RANGE` | Show items from the last time range (default: `1m`)<br>Examples: `1h` (hour), `2d` (days), `3w` (weeks), `4m` (months), `1y` (year) |
| `--debug` | Show detailed API call progress instead of progress bar |
| `--local` | Use local database instead of GitLab API (offline mode, no token required) |
| `--links` | Show hyperlinks underneath each PR and issue |
| `--ll` | Shortcut for `--local --links` (offline mode with links) |
| `--clean` | Delete and recreate the database cache (useful for starting fresh or fixing corrupted cache) |
| `--allowed-repos REPOS` | Filter to specific repositories (comma-separated: `group/repo,group/subgroup/repo`) |

### Color Coding

**Labels:**
- `AUTHORED` - Cyan
- `MENTIONED` - Yellow
- `ASSIGNED` - Magenta
- `COMMENTED` - Blue
- `REVIEWED` - Green
- `REVIEW REQUESTED` - Red
- `INVOLVED` - Gray

**States:**
- `OPEN` - Green
- `CLOSED` - Red
- `MERGED` - Magenta

**Usernames:** Each user gets a consistent color based on hash

## How It Works

### Online Mode (Default)

1. **Scoped Fetching** - Loads merge requests and issues from `ALLOWED_REPOS` project paths
   - Uses GitLab identity to label authored/assigned/reviewed/commented/mentioned activity
   - Applies your `--time` window to keep output focused

2. **Local Caching** - All fetched data is automatically saved to a local BBolt database (`~/.gitlab-feed/gitlab.db`)
   - Merge requests, issues, and notes are cached for offline access
   - Each item is stored/updated with a unique key
   - Database grows as you fetch more data

3. **Cross-Reference Detection** - Automatically finds connections between merge requests and issues
   - Displays linked issues directly under their related merge requests

4. **Smart Filtering**:
   - Shows both open and closed items from the specified time period
   - **Default**: Items updated in last month (`1m`)
   - **Custom**: Use `--time` with values like `1h`, `2d`, `3w`, `6m`, `1y`

### Offline Mode (`--local`)

- Reads all data from the local database instead of GitLab API
- No internet connection or token required
- Displays all cached merge requests and issues
- Useful for:
  - Working offline
  - Faster lookups when you don't need fresh data
  - Reviewing previously fetched data

## API Rate Limits

GitAI monitors API limits and retries automatically when responses indicate throttling.
Rate limit and retry status is displayed in debug mode.

### Automatic Retry & Backoff

When rate limits are hit, GitAI automatically retries with exponential backoff:
- Detects rate limit errors (429, 403 responses)
- Waits progressively longer between retries (1s → 2s → 4s → ... up to 30s max)
- Continues indefinitely until the request succeeds
- Shows clear warnings: `Rate limit hit, waiting [duration] before retry...`
- No manual intervention required - the tool handles rate limits gracefully

## Troubleshooting

### "token is required for GitLab API mode"
Set `GITLAB_TOKEN` (or `GITLAB_ACTIVITY_TOKEN`) as described in [Configuration](#configuration).

### "ALLOWED_REPOS is required for GitLab API mode"
Set `ALLOWED_REPOS` with one or more GitLab project paths in `group[/subgroup]/repo` format.

### "Rate limit exceeded"
Wait for the rate limit to reset. Use `--debug` to see current rate limits.

### Progress bar looks garbled
Your terminal may not support ANSI colors properly. Use `--debug` mode for plain text output.

## Development

### Project Structure
```
gitlab-feed/
├── main.go                      # Main application code
├── db.go                        # Database operations for caching activity data
├── README.md                    # This file
├── CLAUDE.md                    # Instructions for Claude Code AI assistant
├── .goreleaser.yml              # GoReleaser configuration for builds
├── .github/
│   └── workflows/
│       └── release.yml          # GitHub Actions workflow for releases

~/.gitlab-feed/              # Config directory (auto-created)
 ├── .env                     # Configuration file with credentials
 └── gitlab.db                # BBolt database for caching
```

### Testing Releases Locally

You can test the GoReleaser build locally before pushing a tag:

```bash
# Install goreleaser
go install github.com/goreleaser/goreleaser/v2@latest

# Test the build (creates snapshot without publishing)
goreleaser release --snapshot --clean

# Check the dist/ folder for built binaries
ls -la dist/
```

## License

MIT License - Feel free to use and modify as needed.
