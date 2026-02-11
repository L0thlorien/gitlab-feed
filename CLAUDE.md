# GitLab Feed (gitlab-feed)

GitLab Feed is a Go CLI for monitoring GitLab merge requests and issues across a bounded set of projects.

## Build & Run

```bash
go build -o gitlab-feed .

./gitlab-feed
./gitlab-feed --time 3h
./gitlab-feed --debug
./gitlab-feed --local
./gitlab-feed --links
./gitlab-feed --ll
./gitlab-feed --clean
./gitlab-feed --allowed-repos "group/repo,group/subgroup/repo"
```

## Configuration

Online mode requires a GitLab Personal Access Token and `ALLOWED_REPOS`.

The app loads configuration from:
1) Environment variables
2) `~/.gitlab-feed/.env` (auto-created on first run)

Environment variables take precedence over values in the `.env` file.

Environment variables:
- `GITLAB_TOKEN` or `GITLAB_ACTIVITY_TOKEN`
- `GITLAB_HOST` (optional host override; takes precedence over `GITLAB_BASE_URL`)
- `GITLAB_BASE_URL` (optional; default: `https://gitlab.com`)
- `ALLOWED_REPOS` (required online; comma-separated `group[/subgroup]/repo`)

Database cache:
- `~/.gitlab-feed/gitlab.db` (BBolt)

## Testing

```bash
go test ./... -count=1
```
