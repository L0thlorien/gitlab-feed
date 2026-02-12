# GitLab Feed (gitlab-feed)

GitLab Feed is a Go CLI for monitoring GitHub pull requests and GitLab merge requests and issues across a bounded set of projects.

## Build & Run

```bash
go build -o gitlab-feed .

./gitlab-feed
./gitlab-feed --platform github
./gitlab-feed --platform gitlab
./gitlab-feed --time 3h
./gitlab-feed --debug
./gitlab-feed --local
./gitlab-feed --links
./gitlab-feed --ll
./gitlab-feed --clean
./gitlab-feed --allowed-repos "owner/repo,owner/other"
./gitlab-feed --platform gitlab --allowed-repos "group/repo,group/subgroup/repo"
```

## Configuration

Select the platform via `--platform github|gitlab` (default: `github`).

Online mode requirements depend on platform:

- GitHub: `GITHUB_TOKEN`, `GITHUB_USERNAME` (and optionally `GITHUB_ALLOWED_REPOS`)
- GitLab: `GITLAB_TOKEN` (or `GITLAB_ACTIVITY_TOKEN`) and `GITLAB_ALLOWED_REPOS`

The app loads configuration from:
1) Environment variables
2) Shared `.env` file (auto-created on first run)
   - `~/.git-feed/.env`

Precedence order:
1) CLI flags
2) Environment variables
3) Shared `.env` file
4) Built-in defaults

Environment variables:
- GitHub
  - `GITHUB_TOKEN` (required online)
  - `GITHUB_USERNAME` (required online)
  - `GITHUB_ALLOWED_REPOS` (optional; comma-separated `owner/repo`)

- GitLab
  - `GITLAB_TOKEN` or `GITLAB_ACTIVITY_TOKEN` (required online)
  - `GITLAB_HOST` (optional host override; takes precedence over `GITLAB_BASE_URL`)
  - `GITLAB_BASE_URL` (optional; default: `https://gitlab.com`)
  - `GITLAB_ALLOWED_REPOS` (required online; comma-separated `group[/subgroup]/repo`)
  - `ALLOWED_REPOS` (legacy fallback for either platform when platform-specific vars are unset)
  - `GITLAB_USERNAME` or `GITLAB_USER` (optional legacy override; user is normally auto-resolved via API)

Token scopes:
- `read_api` (recommended)
- `api` only if your self-managed instance requires broader scope

Reference: https://docs.gitlab.com/user/profile/personal_access_tokens/

Database cache:
- GitHub: `~/.git-feed/github.db` (BBolt)
- GitLab: `~/.git-feed/gitlab.db` (BBolt)

## Testing

```bash
go test ./... -count=1
```
