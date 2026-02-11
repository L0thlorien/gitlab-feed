package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"hash/fnv"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/fatih/color"
	gitlab "gitlab.com/gitlab-org/api/client-go"
)

type PRActivity struct {
	Label      string
	Owner      string
	Repo       string
	MR         MergeRequestModel
	UpdatedAt  time.Time
	HasUpdates bool
	Issues     []IssueActivity
}

type IssueActivity struct {
	Label      string
	Owner      string
	Repo       string
	Issue      IssueModel
	UpdatedAt  time.Time
	HasUpdates bool
}

type MergeRequestModel struct {
	Number    int
	Title     string
	Body      string
	State     string
	UpdatedAt time.Time
	WebURL    string
	UserLogin string
	Merged    bool
}

type IssueModel struct {
	Number    int
	Title     string
	Body      string
	State     string
	UpdatedAt time.Time
	WebURL    string
	UserLogin string
}

type CommentModel struct {
	Body string
}

type Progress struct {
	current atomic.Int32
	total   atomic.Int32
}

type Config struct {
	debugMode        bool
	localMode        bool
	gitlabUserID     int64
	showLinks        bool
	timeRange        time.Duration
	gitlabUsername   string
	allowedRepos     map[string]bool
	gitlabClient     *gitlab.Client
	db               *Database
	progress         *Progress
	ctx              context.Context
	dbErrorCount     atomic.Int32
}

var config Config

var retryAfter = time.After

const defaultGitLabBaseURL = "https://gitlab.com"

func normalizeGitLabBaseURL(raw string) (string, error) {
	baseURL := strings.TrimSpace(raw)
	if baseURL == "" {
		baseURL = defaultGitLabBaseURL
	}

	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("invalid GitLab base URL %q: %w", baseURL, err)
	}

	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid GitLab base URL %q: must include scheme and host", baseURL)
	}

	normalizedPath := strings.TrimSuffix(parsed.EscapedPath(), "/")
	if normalizedPath == "" {
		normalizedPath = "/api/v4"
	} else if !strings.HasSuffix(normalizedPath, "/api/v4") {
		normalizedPath += "/api/v4"
	}

	parsed.Path = normalizedPath
	parsed.RawPath = ""

	return parsed.String(), nil
}

func newGitLabClient(token, rawBaseURL string) (*gitlab.Client, string, error) {
	normalizedBaseURL, err := normalizeGitLabBaseURL(rawBaseURL)
	if err != nil {
		return nil, "", err
	}

	client, err := gitlab.NewClient(token, gitlab.WithBaseURL(normalizedBaseURL))
	if err != nil {
		return nil, "", fmt.Errorf("failed to create GitLab client: %w", err)
	}

	return client, normalizedBaseURL, nil
}

func getPRLabelPriority(label string) int {
	priorities := map[string]int{
		"Authored":         1,
		"Assigned":         2,
		"Reviewed":         3,
		"Review Requested": 4,
		"Commented":        5,
		"Mentioned":        6,
	}
	if priority, ok := priorities[label]; ok {
		return priority
	}
	return 999 // Unknown labels get lowest priority
}

func getIssueLabelPriority(label string) int {
	priorities := map[string]int{
		"Authored":  1,
		"Assigned":  2,
		"Commented": 3,
		"Mentioned": 4,
	}
	if priority, ok := priorities[label]; ok {
		return priority
	}
	return 999 // Unknown labels get lowest priority
}

func shouldUpdateLabel(currentLabel, newLabel string, isPR bool) bool {
	if currentLabel == "" {
		return true
	}

	var currentPriority, newPriority int
	if isPR {
		currentPriority = getPRLabelPriority(currentLabel)
		newPriority = getPRLabelPriority(newLabel)
	} else {
		currentPriority = getIssueLabelPriority(currentLabel)
		newPriority = getIssueLabelPriority(newLabel)
	}

	return newPriority < currentPriority
}

func (p *Progress) increment() {
	p.current.Add(1)
}

func (p *Progress) addToTotal(n int) {
	p.total.Add(int32(n))
}

func (p *Progress) buildBar(current, total int32) (string, *color.Color, float64) {
	percentage := float64(current) / float64(total) * 100
	filled := int(percentage / 2)
	var barContent string
	for i := range 50 {
		if i < filled {
			barContent += "="
		} else if i == filled {
			barContent += ">"
		} else {
			barContent += " "
		}
	}
	var barColor *color.Color
	if percentage < 33 {
		barColor = color.New(color.FgRed)
	} else if percentage < 66 {
		barColor = color.New(color.FgYellow)
	} else {
		barColor = color.New(color.FgGreen)
	}
	return barContent, barColor, percentage
}

func (p *Progress) display() {
	current := p.current.Load()
	total := p.total.Load()
	barContent, barColor, percentage := p.buildBar(current, total)
	fmt.Printf("\r[%s] %s/%s (%s) ",
		barColor.Sprint(barContent),
		color.New(color.FgCyan).Sprint(current),
		color.New(color.FgCyan).Sprint(total),
		barColor.Sprintf("%.0f%%", percentage))
}

func (p *Progress) displayWithWarning(message string) {
	current := p.current.Load()
	total := p.total.Load()
	barContent, barColor, percentage := p.buildBar(current, total)
	fmt.Printf("\r[%s] %s/%s (%s) %s ",
		barColor.Sprint(barContent),
		color.New(color.FgCyan).Sprint(current),
		color.New(color.FgCyan).Sprint(total),
		barColor.Sprintf("%.0f%%", percentage),
		color.New(color.FgYellow).Sprint("! "+message))
}

func retryWithBackoff(operation func() error, operationName string) error {
	const (
		initialBackoff = 1 * time.Second
		maxBackoff     = 30 * time.Second
		backoffFactor  = 1.5
	)

	backoff := initialBackoff
	attempt := 1
	retryCtx := config.ctx
	if retryCtx == nil {
		retryCtx = context.Background()
	}

	for {
		err := operation()
		if err == nil {
			return nil
		}

		var gitLabErr *gitlab.ErrorResponse
		var waitTime time.Duration
		var isRateLimitError bool
		var isTransientServerError bool
		shouldRetry := true

		if errors.As(err, &gitLabErr) && gitLabErr.Response != nil {
			statusCode := gitLabErr.Response.StatusCode

			if statusCode == http.StatusTooManyRequests {
				isRateLimitError = true
				retryAfterSeconds, parseErr := strconv.Atoi(strings.TrimSpace(gitLabErr.Response.Header.Get("Retry-After")))
				if parseErr == nil && retryAfterSeconds > 0 {
					waitTime = time.Duration(retryAfterSeconds) * time.Second
				} else if resetWait, ok := gitLabRateLimitResetWait(gitLabErr.Response.Header.Get("Ratelimit-Reset")); ok {
					waitTime = resetWait
				} else {
					waitTime = time.Duration(math.Min(float64(backoff), float64(maxBackoff)))
				}

				if config.debugMode {
					fmt.Printf("  [%s] GitLab rate limit hit (attempt %d), waiting %v before retry...\n",
						operationName, attempt, waitTime.Round(time.Second))
				}
			} else if statusCode >= http.StatusInternalServerError && statusCode <= 599 {
				isTransientServerError = true
				waitTime = time.Duration(math.Min(float64(backoff), float64(maxBackoff)))

				if config.debugMode {
					fmt.Printf("  [%s] GitLab server error %d (attempt %d), waiting %v before retry...\n",
						operationName, statusCode, attempt, waitTime)
				}
			} else {
				shouldRetry = false
			}
			} else {
			isRateLimitError = strings.Contains(err.Error(), "rate limit") ||
				strings.Contains(err.Error(), "API rate limit exceeded") ||
				strings.Contains(err.Error(), "403")

			if isRateLimitError {
				// Fallback to exponential backoff if we can't extract reset time
				waitTime = time.Duration(math.Min(float64(backoff), float64(maxBackoff)))
				if config.debugMode {
					fmt.Printf("  [%s] Rate limit hit (attempt %d), waiting %v before retry...\n",
						operationName, attempt, waitTime)
				}
			}
		}

		if !shouldRetry {
			return err
		}

		if isRateLimitError {
			if config.debugMode {
				select {
				case <-retryCtx.Done():
					return retryCtx.Err()
				case <-retryAfter(waitTime):
				}
			} else {
				ticker := time.NewTicker(1 * time.Second)
				defer ticker.Stop()

				remaining := int(waitTime.Seconds())
				for remaining > 0 {
					if config.progress != nil {
						config.progress.displayWithWarning(fmt.Sprintf("Rate limit hit, retrying in %ds", remaining))
					}

					select {
					case <-retryCtx.Done():
						return retryCtx.Err()
					case <-ticker.C:
						remaining--
					}
				}
			}

			backoff = time.Duration(float64(backoff) * backoffFactor)
		} else if isTransientServerError {
			if config.debugMode {
				select {
				case <-retryCtx.Done():
					return retryCtx.Err()
				case <-retryAfter(waitTime):
				}
			} else {
				ticker := time.NewTicker(1 * time.Second)
				defer ticker.Stop()

				remaining := int(waitTime.Seconds())
				for remaining > 0 {
					if config.progress != nil {
						config.progress.displayWithWarning(fmt.Sprintf("API error, retrying in %ds", remaining))
					}

					select {
					case <-retryCtx.Done():
						return retryCtx.Err()
					case <-ticker.C:
						remaining--
					}
				}
			}

			backoff = time.Duration(float64(backoff) * backoffFactor)
		} else {
			// Non-rate-limit error - use exponential backoff
			waitTime := time.Duration(math.Min(float64(backoff)/2, float64(5*time.Second)))

			if config.debugMode {
				fmt.Printf("  [%s] Error (attempt %d): %v, waiting %v before retry...\n",
					operationName, attempt, err, waitTime)
				select {
				case <-retryCtx.Done():
					return retryCtx.Err()
				case <-retryAfter(waitTime):
				}
			} else {
				ticker := time.NewTicker(1 * time.Second)
				defer ticker.Stop()

				remaining := int(waitTime.Seconds())
				for remaining > 0 {
					if config.progress != nil {
						config.progress.displayWithWarning(fmt.Sprintf("API error, retrying in %ds", remaining))
					}

					select {
					case <-retryCtx.Done():
						return retryCtx.Err()
					case <-ticker.C:
						remaining--
					}
				}
			}

			backoff = time.Duration(float64(backoff) * backoffFactor)
		}

		attempt++
	}
}

func gitLabRateLimitResetWait(rawHeader string) (time.Duration, bool) {
	resetAtUnix, err := strconv.ParseInt(strings.TrimSpace(rawHeader), 10, 64)
	if err != nil || resetAtUnix <= 0 {
		return 0, false
	}

	resetTime := time.Unix(resetAtUnix, 0)
	waitTime := time.Until(resetTime)
	if waitTime <= 0 {
		return 1 * time.Second, true
	}

	return waitTime, true
}

func getLabelColor(label string) *color.Color {
	labelColors := map[string]*color.Color{
		"Authored":         color.New(color.FgCyan),
		"Mentioned":        color.New(color.FgYellow),
		"Assigned":         color.New(color.FgMagenta),
		"Commented":        color.New(color.FgBlue),
		"Reviewed":         color.New(color.FgGreen),
		"Review Requested": color.New(color.FgRed),
		"Involved":         color.New(color.FgHiBlack),
		"Recent Activity":  color.New(color.FgHiCyan),
	}

	if c, ok := labelColors[label]; ok {
		return c
	}
	return color.New(color.FgWhite)
}

func getUserColor(username string) *color.Color {
	h := fnv.New32a()
	h.Write([]byte(username))
	hash := h.Sum32()

	colors := []*color.Color{
		color.New(color.FgHiGreen),
		color.New(color.FgHiYellow),
		color.New(color.FgHiBlue),
		color.New(color.FgHiMagenta),
		color.New(color.FgHiCyan),
		color.New(color.FgHiRed),
		color.New(color.FgGreen),
		color.New(color.FgYellow),
		color.New(color.FgBlue),
		color.New(color.FgMagenta),
		color.New(color.FgCyan),
	}

	return colors[hash%uint32(len(colors))]
}

func getStateColor(state string) *color.Color {
	switch state {
	case "open":
		return color.New(color.FgGreen)
	case "closed":
		return color.New(color.FgRed)
	case "merged":
		return color.New(color.FgMagenta)
	default:
		return color.New(color.FgWhite)
	}
}

func loadEnvFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			value := strings.TrimSpace(parts[1])
			if _, exists := os.LookupEnv(key); exists {
				continue
			}
			os.Setenv(key, value)
		}
	}

	return scanner.Err()
}

func parseTimeRange(timeStr string) (time.Duration, error) {
	if len(timeStr) < 2 {
		return 0, fmt.Errorf("invalid time range format: %s (expected format like 1h, 2d, 3w, 4m, 1y)", timeStr)
	}

	numStr := timeStr[:len(timeStr)-1]
	unit := timeStr[len(timeStr)-1:]

	num, err := strconv.Atoi(numStr)
	if err != nil || num < 1 {
		return 0, fmt.Errorf("invalid time range number: %s (must be a positive integer)", numStr)
	}

	var duration time.Duration
	switch unit {
	case "h":
		duration = time.Duration(num) * time.Hour
	case "d":
		duration = time.Duration(num) * 24 * time.Hour
	case "w":
		duration = time.Duration(num) * 7 * 24 * time.Hour
	case "m":
		duration = time.Duration(num) * 30 * 24 * time.Hour
	case "y":
		duration = time.Duration(num) * 365 * 24 * time.Hour
	default:
		return 0, fmt.Errorf("invalid time unit: %s (use h=hours, d=days, w=weeks, m=months, y=years)", unit)
	}

	return duration, nil
}

func main() {
	// Define flags
	var timeRangeStr string
	var debugMode bool
	var localMode bool
	var showLinks bool
	var llMode bool
	var allowedReposFlag string
	var cleanCache bool

	flag.StringVar(&timeRangeStr, "time", "1m", "Show items from last time range (1h, 2d, 3w, 4m, 1y)")
	flag.BoolVar(&debugMode, "debug", false, "Show detailed API logging")
	flag.BoolVar(&localMode, "local", false, "Use local database instead of GitLab API")
	flag.BoolVar(&showLinks, "links", false, "Show hyperlinks underneath each PR/issue")
	flag.BoolVar(&llMode, "ll", false, "Shortcut for --local --links (offline mode with links)")
	flag.BoolVar(&cleanCache, "clean", false, "Delete and recreate the database cache")
	flag.StringVar(&allowedReposFlag, "allowed-repos", "", "Comma-separated list of allowed repos (e.g., group/repo,group/subgroup/repo)")

	// Custom usage message
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [options]\n\n", os.Args[0])
		fmt.Fprintln(os.Stderr, "GitLab Feed - Monitor pull requests and issues across repositories")
		fmt.Fprintln(os.Stderr, "\nOptions:")
		flag.PrintDefaults()
		fmt.Fprintln(os.Stderr, "\nEnvironment Variables:")
		fmt.Fprintln(os.Stderr, "  GITLAB_TOKEN or GITLAB_ACTIVITY_TOKEN  - GitLab Personal Access Token")
		fmt.Fprintln(os.Stderr, "  GITLAB_USERNAME or GITLAB_USER         - Optional GitLab username")
		fmt.Fprintln(os.Stderr, "  GITLAB_HOST                            - Optional GitLab host (overrides GITLAB_BASE_URL when set)")
		fmt.Fprintln(os.Stderr, "  GITLAB_BASE_URL                        - Optional GitLab base URL (default: https://gitlab.com)")
		fmt.Fprintln(os.Stderr, "  ALLOWED_REPOS                          - Required in online mode (group[/subgroup]/repo)")
		fmt.Fprintln(os.Stderr, "\nConfiguration File:")
		fmt.Fprintln(os.Stderr, "  ~/.gitlab-feed/.env                    - Configuration file (auto-created)")
	}

	flag.Parse()

	// Handle --ll shortcut
	if llMode {
		localMode = true
		showLinks = true
	}

	// Parse time range
	timeRange, err := parseTimeRange(timeRangeStr)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		fmt.Println("Examples: --time 1h (1 hour), --time 2d (2 days), --time 3w (3 weeks), --time 4m (4 months), --time 1y (1 year)")
		os.Exit(1)
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Printf("Error: Could not determine home directory: %v\n", err)
		os.Exit(1)
	}

	configDir := filepath.Join(homeDir, ".gitlab-feed")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		fmt.Printf("Error: Could not create config directory %s: %v\n", configDir, err)
		os.Exit(1)
	}

	envPath := filepath.Join(configDir, ".env")
	if _, err := os.Stat(envPath); os.IsNotExist(err) {
		envTemplate := `# Activity Feed Configuration
# Add your API credentials here

# Your GitLab Personal Access Token (required for online mode)
# Recommended scope: read_api (or api on some self-managed instances)
GITLAB_TOKEN=

# Optional username (the app also resolves the current user via API)
GITLAB_USERNAME=

		# Optional: GitLab host for self-managed/cloud instances
		# Example: http://10.10.1.207/
		# If set, this overrides GITLAB_BASE_URL.
		GITLAB_HOST=

		# Optional: full GitLab base URL (supports path prefixes)
		# Default: https://gitlab.com
		GITLAB_BASE_URL=https://gitlab.com

# Required in online mode: comma-separated allowed repos
# Format: group/repo or group/subgroup/repo
# Example self-managed repo path: platform/backend/gitlab-feed
ALLOWED_REPOS=
`
		if err := os.WriteFile(envPath, []byte(envTemplate), 0o600); err != nil {
			fmt.Printf("Warning: Could not create .env file at %s: %v\n", envPath, err)
		}
	}

	_ = loadEnvFile(envPath)

	allowedReposStr := allowedReposFlag
	if allowedReposStr == "" {
		allowedReposStr = os.Getenv("ALLOWED_REPOS")
	}

	var allowedRepos map[string]bool
	if allowedReposStr != "" {
		allowedRepos = make(map[string]bool)
		repos := strings.Split(allowedReposStr, ",")
		for _, repo := range repos {
			repo = strings.TrimSpace(repo)
			if repo != "" {
				allowedRepos[repo] = true
			}
		}
		if debugMode && len(allowedRepos) > 0 {
			fmt.Printf("Filtering to allowed repositories: %v\n", allowedRepos)
		}
	}

	dbPath := filepath.Join(configDir, "gitlab.db")

	if cleanCache {
		fmt.Println("Cleaning database cache...")
		if _, err := os.Stat(dbPath); err == nil {
			if err := os.Remove(dbPath); err != nil {
				fmt.Printf("Warning: Failed to delete database file: %v\n", err)
			} else {
				fmt.Println("Database cache cleaned successfully")
			}
		} else {
			fmt.Println("No existing database cache to clean")
		}
	}

	db, err := OpenDatabase(dbPath)
	if err != nil {
		fmt.Printf("Warning: Failed to open database: %v\n", err)
		fmt.Println("Continuing without database caching...")
		db = nil
	} else {
		defer db.Close()
	}

	token := os.Getenv("GITLAB_ACTIVITY_TOKEN")
	if token == "" {
		token = os.Getenv("GITLAB_TOKEN")
	}

	rawGitLabHost := os.Getenv("GITLAB_HOST")
	rawGitLabBaseURL := os.Getenv("GITLAB_BASE_URL")
	selectedGitLabBaseURL := rawGitLabBaseURL
	if strings.TrimSpace(rawGitLabHost) != "" {
		selectedGitLabBaseURL = rawGitLabHost
	}

	normalizedGitLabBaseURL, err := normalizeGitLabBaseURL(selectedGitLabBaseURL)
	if err != nil {
		if strings.TrimSpace(selectedGitLabBaseURL) != "" {
			fmt.Printf("Configuration Error: %v\n", err)
			os.Exit(1)
		}

		normalizedGitLabBaseURL, _ = normalizeGitLabBaseURL("")
	}

	var gitlabClient *gitlab.Client
	gitlabUsername := ""
	var gitlabUserID int64
	if !localMode && token != "" {
		client, _, err := newGitLabClient(token, selectedGitLabBaseURL)
		if err != nil {
			fmt.Printf("Configuration Error: %v\n", err)
			os.Exit(1)
		}
		gitlabClient = client

		currentUser, _, err := gitlabClient.Users.CurrentUser(gitlab.WithContext(context.Background()))
		if err != nil {
			fmt.Printf("Configuration Error: failed to fetch GitLab current user: %v\n", err)
			os.Exit(1)
		}
		gitlabUsername = strings.TrimSpace(currentUser.Username)
		gitlabUserID = currentUser.ID
		if gitlabUsername == "" {
			fmt.Println("Configuration Error: GitLab current user has empty username")
			os.Exit(1)
		}
	}

	// Validate configuration
	if err := validateConfig(token, localMode, envPath, allowedRepos); err != nil {
		fmt.Printf("Configuration Error: %v\n\n", err)
		os.Exit(1)
	}

	if debugMode {
		fmt.Println("Monitoring GitLab merge request and issue activity")
		fmt.Printf("Showing items from the last %v\n", timeRange)
		fmt.Printf("GitLab API base URL: %s\n", normalizedGitLabBaseURL)
	}
	if debugMode {
		fmt.Println("Debug mode enabled")
	}

	config.debugMode = debugMode
	config.localMode = localMode
	config.gitlabUserID = gitlabUserID
	config.showLinks = showLinks
	config.timeRange = timeRange
	config.gitlabUsername = gitlabUsername
	config.allowedRepos = allowedRepos
	config.db = db
	config.ctx = context.Background()
	config.gitlabClient = gitlabClient

	fetchAndDisplayActivity()
}

func validateConfig(token string, localMode bool, envPath string, allowedRepos map[string]bool) error {
	if localMode {
		return nil // No validation needed for offline mode
	}

	if token == "" {
		return fmt.Errorf("token is required for GitLab API mode.\n\nTo fix this:\n  - Set GITLAB_TOKEN or GITLAB_ACTIVITY_TOKEN\n  - Or add it to %s", envPath)
	}
	if len(allowedRepos) == 0 {
		return fmt.Errorf("ALLOWED_REPOS is required for GitLab API mode to keep API usage bounded.\n\nTo fix this:\n  - Set ALLOWED_REPOS with group[/subgroup]/repo paths\n  - Example: ALLOWED_REPOS=team/service,platform/backend/gitlab-feed\n  - Or add it to %s", envPath)
	}
	return nil
}

func fetchAndDisplayActivity() {
	fetchAndDisplayGitLabActivity()
}

type DisplayConfig struct {
	Owner      string
	Repo       string
	Number     int
	Title      string
	User       string
	UpdatedAt  time.Time
	WebURL     string
	Label      string
	HasUpdates bool
	IsIndented bool
	State      string
}

func displayItem(cfg DisplayConfig) {
	dateStr := "          "
	if !cfg.UpdatedAt.IsZero() {
		dateStr = cfg.UpdatedAt.Format("2006/01/02")
	}

	indent := ""
	linkIndent := "   "
	if cfg.IsIndented && cfg.State != "" {
		state := strings.ToUpper(cfg.State)
		stateColor := getStateColor(cfg.State)
		indent = fmt.Sprintf("-- %s ", stateColor.Sprint(state))
		linkIndent = "      "
	}

	labelColor := getLabelColor(cfg.Label)
	userColor := getUserColor(cfg.User)

	updateIcon := ""
	if cfg.HasUpdates {
		updateIcon = color.New(color.FgYellow, color.Bold).Sprint("● ")
	}

	repoDisplay := ""
	if cfg.Repo == "" {
		repoDisplay = fmt.Sprintf("%s#%d", cfg.Owner, cfg.Number)
	} else {
		repoDisplay = fmt.Sprintf("%s/%s#%d", cfg.Owner, cfg.Repo, cfg.Number)
	}

	fmt.Printf("%s%s%s %s %s %s - %s\n",
		updateIcon,
		indent,
		dateStr,
		labelColor.Sprint(strings.ToUpper(cfg.Label)),
		userColor.Sprint(cfg.User),
		repoDisplay,
		cfg.Title,
	)

	if config.showLinks && cfg.WebURL != "" {
		fmt.Printf("%s🔗 %s\n", linkIndent, cfg.WebURL)
	}
}

func displayMergeRequest(label, owner, repo string, mr MergeRequestModel, hasUpdates bool) {
	displayItem(DisplayConfig{
		Owner:      owner,
		Repo:       repo,
		Number:     mr.Number,
		Title:      mr.Title,
		User:       mr.UserLogin,
		UpdatedAt:  mr.UpdatedAt,
		WebURL:     mr.WebURL,
		Label:      label,
		HasUpdates: hasUpdates,
		IsIndented: false,
	})
}

func displayIssue(label, owner, repo string, issue IssueModel, indented bool, hasUpdates bool) {
	displayItem(DisplayConfig{
		Owner:      owner,
		Repo:       repo,
		Number:     issue.Number,
		Title:      issue.Title,
		User:       issue.UserLogin,
		UpdatedAt:  issue.UpdatedAt,
		WebURL:     issue.WebURL,
		Label:      label,
		HasUpdates: hasUpdates,
		IsIndented: indented,
		State:      issue.State,
	})
}

type gitLabProject struct {
	PathWithNamespace string
	ID                int64
}

func fetchAndDisplayGitLabActivity() {
	startTime := time.Now()

	if config.debugMode {
		fmt.Println("Fetching data from GitLab...")
	} else {
		fmt.Print("Fetching data from GitLab... ")
	}

	cutoffTime := time.Now().Add(-config.timeRange)
	var (
		activities      []PRActivity
		issueActivities []IssueActivity
		err             error
	)

	if config.localMode {
		activities, issueActivities, err = loadGitLabCachedActivities(cutoffTime)
	} else {
		activities, issueActivities, err = fetchGitLabProjectActivities(
			config.ctx,
			config.gitlabClient,
			config.allowedRepos,
			cutoffTime,
			config.gitlabUsername,
			config.gitlabUserID,
			config.db,
		)
	}
	if err != nil {
		fmt.Printf("Error fetching GitLab activity: %v\n", err)
		return
	}

	if config.debugMode {
		fmt.Println()
		fmt.Printf("Total fetch time: %v\n", time.Since(startTime).Round(time.Millisecond))
		fmt.Printf("Found %d unique merge requests and %d unique issues\n", len(activities), len(issueActivities))
		fmt.Println()
	} else {
		fmt.Print("\r" + strings.Repeat(" ", 80) + "\r")
	}

	if len(activities) == 0 && len(issueActivities) == 0 {
		fmt.Println("No open activity found")
		return
	}

	sort.Slice(activities, func(i, j int) bool {
		return activities[i].UpdatedAt.After(activities[j].UpdatedAt)
	})
	sort.Slice(issueActivities, func(i, j int) bool {
		return issueActivities[i].UpdatedAt.After(issueActivities[j].UpdatedAt)
	})

	var openPRs, closedPRs, mergedPRs []PRActivity
	for _, activity := range activities {
		if activity.MR.State == "closed" {
			if activity.MR.Merged {
				mergedPRs = append(mergedPRs, activity)
			} else {
				closedPRs = append(closedPRs, activity)
			}
		} else {
			openPRs = append(openPRs, activity)
		}
	}

	var openIssues, closedIssues []IssueActivity
	for _, issue := range issueActivities {
		if issue.Issue.State == "closed" {
			closedIssues = append(closedIssues, issue)
		} else {
			openIssues = append(openIssues, issue)
		}
	}

	if len(openPRs) > 0 {
		titleColor := color.New(color.FgHiGreen, color.Bold)
		fmt.Println(titleColor.Sprint("OPEN PULL REQUESTS:"))
		fmt.Println("------------------------------------------")
		for _, activity := range openPRs {
			displayMergeRequest(activity.Label, activity.Owner, activity.Repo, activity.MR, activity.HasUpdates)
			if len(activity.Issues) > 0 {
				for _, issue := range activity.Issues {
					displayIssue(issue.Label, issue.Owner, issue.Repo, issue.Issue, true, issue.HasUpdates)
				}
			}
		}
	}

	if len(closedPRs) > 0 || len(mergedPRs) > 0 {
		fmt.Println()
		titleColor := color.New(color.FgHiRed, color.Bold)
		fmt.Println(titleColor.Sprint("CLOSED/MERGED PULL REQUESTS:"))
		fmt.Println("------------------------------------------")
		for _, activity := range mergedPRs {
			displayMergeRequest(activity.Label, activity.Owner, activity.Repo, activity.MR, activity.HasUpdates)
			if len(activity.Issues) > 0 {
				for _, issue := range activity.Issues {
					displayIssue(issue.Label, issue.Owner, issue.Repo, issue.Issue, true, issue.HasUpdates)
				}
			}
		}
		for _, activity := range closedPRs {
			displayMergeRequest(activity.Label, activity.Owner, activity.Repo, activity.MR, activity.HasUpdates)
			if len(activity.Issues) > 0 {
				for _, issue := range activity.Issues {
					displayIssue(issue.Label, issue.Owner, issue.Repo, issue.Issue, true, issue.HasUpdates)
				}
			}
		}
	}

	if len(openIssues) > 0 {
		fmt.Println()
		titleColor := color.New(color.FgHiGreen, color.Bold)
		fmt.Println(titleColor.Sprint("OPEN ISSUES:"))
		fmt.Println("------------------------------------------")
		for _, issue := range openIssues {
			displayIssue(issue.Label, issue.Owner, issue.Repo, issue.Issue, false, issue.HasUpdates)
		}
	}

	if len(closedIssues) > 0 {
		fmt.Println()
		titleColor := color.New(color.FgHiRed, color.Bold)
		fmt.Println(titleColor.Sprint("CLOSED ISSUES:"))
		fmt.Println("------------------------------------------")
		for _, issue := range closedIssues {
			displayIssue(issue.Label, issue.Owner, issue.Repo, issue.Issue, false, issue.HasUpdates)
		}
	}
}

func fetchGitLabProjectActivities(
	ctx context.Context,
	client *gitlab.Client,
	allowedRepos map[string]bool,
	cutoff time.Time,
	currentUsername string,
	currentUserID int64,
	db *Database,
) ([]PRActivity, []IssueActivity, error) {
	projects, err := resolveAllowedGitLabProjects(ctx, client, allowedRepos)
	if err != nil {
		return nil, nil, err
	}

	currentUsername = strings.TrimSpace(currentUsername)
	if currentUsername == "" {
		return nil, nil, fmt.Errorf("gitlab current username is required")
	}

	if len(projects) == 0 {
		return []PRActivity{}, []IssueActivity{}, nil
	}

	activities := make([]PRActivity, 0)
	issueActivities := make([]IssueActivity, 0)
	seenMergeRequests := make(map[string]struct{})
	seenIssues := make(map[string]struct{})
	projectIDByPath := make(map[string]int64, len(projects))
	mrNotesByKey := make(map[string][]*gitlab.Note)

	for _, project := range projects {
		projectIDByPath[normalizeProjectPathWithNamespace(project.PathWithNamespace)] = project.ID
	}

	for _, project := range projects {
		projectMergeRequests, err := listGitLabProjectMergeRequests(ctx, client, project.ID, cutoff)
		if err != nil {
			return nil, nil, fmt.Errorf("list merge requests for %s: %w", project.PathWithNamespace, err)
		}

		for _, item := range projectMergeRequests {
			key := buildGitLabDedupKey(project.PathWithNamespace, "mr", item.IID)
			if _, exists := seenMergeRequests[key]; exists {
				continue
			}
			seenMergeRequests[key] = struct{}{}

			model := toMergeRequestModelFromGitLab(item)
			if model.UpdatedAt.IsZero() || model.UpdatedAt.Before(cutoff) {
				continue
			}

			label, notes, err := deriveGitLabMergeRequestLabel(ctx, client, project.ID, item, currentUsername, currentUserID)
			if err != nil {
				return nil, nil, fmt.Errorf("derive merge request label for %s!%d: %w", project.PathWithNamespace, item.IID, err)
			}

			if db != nil {
				if err := db.SaveGitLabMergeRequestWithLabel(project.PathWithNamespace, model, label, config.debugMode); err != nil {
					config.dbErrorCount.Add(1)
					if config.debugMode {
						fmt.Printf("  [DB] Warning: Failed to save GitLab MR %s!%d: %v\n", project.PathWithNamespace, item.IID, err)
					}
				}
				if err := persistGitLabNotes(db, project.PathWithNamespace, "mr", int(item.IID), notes); err != nil {
					config.dbErrorCount.Add(1)
					if config.debugMode {
						fmt.Printf("  [DB] Warning: Failed to save GitLab MR notes %s!%d: %v\n", project.PathWithNamespace, item.IID, err)
					}
				}
			}

			mrNotesByKey[buildGitLabMergeRequestKey(project.PathWithNamespace, model.Number)] = notes

			activities = append(activities, PRActivity{
				Label:     label,
				Owner:     project.PathWithNamespace,
				Repo:      "",
				MR:        model,
				UpdatedAt: model.UpdatedAt,
			})
		}

		projectIssues, err := listGitLabProjectIssues(ctx, client, project.ID, cutoff)
		if err != nil {
			return nil, nil, fmt.Errorf("list issues for %s: %w", project.PathWithNamespace, err)
		}

		for _, item := range projectIssues {
			key := buildGitLabDedupKey(project.PathWithNamespace, "issue", item.IID)
			if _, exists := seenIssues[key]; exists {
				continue
			}
			seenIssues[key] = struct{}{}

			model := toIssueModelFromGitLab(item)
			if model.UpdatedAt.IsZero() || model.UpdatedAt.Before(cutoff) {
				continue
			}

			label, notes, err := deriveGitLabIssueLabel(ctx, client, project.ID, item, currentUsername, currentUserID)
			if err != nil {
				return nil, nil, fmt.Errorf("derive issue label for %s#%d: %w", project.PathWithNamespace, item.IID, err)
			}

			if db != nil {
				if err := db.SaveGitLabIssueWithLabel(project.PathWithNamespace, model, label, config.debugMode); err != nil {
					config.dbErrorCount.Add(1)
					if config.debugMode {
						fmt.Printf("  [DB] Warning: Failed to save GitLab issue %s#%d: %v\n", project.PathWithNamespace, item.IID, err)
					}
				}
				if err := persistGitLabNotes(db, project.PathWithNamespace, "issue", int(item.IID), notes); err != nil {
					config.dbErrorCount.Add(1)
					if config.debugMode {
						fmt.Printf("  [DB] Warning: Failed to save GitLab issue notes %s#%d: %v\n", project.PathWithNamespace, item.IID, err)
					}
				}
			}

			issueActivities = append(issueActivities, IssueActivity{
				Label:     label,
				Owner:     project.PathWithNamespace,
				Repo:      "",
				Issue:     model,
				UpdatedAt: model.UpdatedAt,
			})
		}
	}

	activities, issueActivities, err = linkGitLabCrossReferencesOnline(ctx, client, activities, issueActivities, projectIDByPath, mrNotesByKey, db)
	if err != nil {
		return nil, nil, err
	}

	return activities, issueActivities, nil
}

func deriveGitLabMergeRequestLabel(
	ctx context.Context,
	client *gitlab.Client,
	projectID int64,
	item *gitlab.BasicMergeRequest,
	currentUsername string,
	currentUserID int64,
) (string, []*gitlab.Note, error) {
	if item == nil {
		return "Involved", nil, nil
	}

	currentLabel := ""
	if matchesGitLabBasicUser(item.Author, currentUsername, currentUserID) {
		currentLabel = mergeLabelWithPriority(currentLabel, "Authored", true)
	}
	if gitLabBasicUserListContains(item.Assignees, currentUsername, currentUserID) || matchesGitLabBasicUser(item.Assignee, currentUsername, currentUserID) {
		currentLabel = mergeLabelWithPriority(currentLabel, "Assigned", true)
	}

	if currentLabel == "Authored" || currentLabel == "Assigned" {
		return currentLabel, nil, nil
	}

	var approvalState *gitlab.MergeRequestApprovalState
	err := retryWithBackoff(func() error {
		var apiErr error
		approvalState, _, apiErr = client.MergeRequestApprovals.GetApprovalState(projectID, item.IID, gitlab.WithContext(ctx))
		return apiErr
	}, fmt.Sprintf("GitLabGetApprovalState %d!%d", projectID, item.IID))
	if err != nil {
		return "", nil, err
	}
	if gitLabApprovalStateReviewedByCurrentUser(approvalState, currentUsername, currentUserID) {
		currentLabel = mergeLabelWithPriority(currentLabel, "Reviewed", true)
	}

	if gitLabBasicUserListContains(item.Reviewers, currentUsername, currentUserID) {
		currentLabel = mergeLabelWithPriority(currentLabel, "Review Requested", true)
	}

	if !needsLowerPriorityPRChecks(currentLabel) {
		if currentLabel == "" {
			return "Involved", nil, nil
		}
		return currentLabel, nil, nil
	}

	notes, err := listAllGitLabMergeRequestNotes(ctx, client, projectID, item.IID)
	if err != nil {
		return "", nil, err
	}

	commented, mentioned := gitLabNotesInvolvement(notes, item.Description, currentUsername, currentUserID)
	if commented {
		currentLabel = mergeLabelWithPriority(currentLabel, "Commented", true)
	}
	if mentioned {
		currentLabel = mergeLabelWithPriority(currentLabel, "Mentioned", true)
	}

	if currentLabel == "" {
		return "Involved", notes, nil
	}
	return currentLabel, notes, nil
}

func deriveGitLabIssueLabel(
	ctx context.Context,
	client *gitlab.Client,
	projectID int64,
	item *gitlab.Issue,
	currentUsername string,
	currentUserID int64,
) (string, []*gitlab.Note, error) {
	if item == nil {
		return "Involved", nil, nil
	}

	currentLabel := ""
	if matchesGitLabIssueAuthor(item.Author, currentUsername, currentUserID) {
		currentLabel = mergeLabelWithPriority(currentLabel, "Authored", false)
	}
	if gitLabIssueAssigneeListContains(item.Assignees, currentUsername, currentUserID) || matchesGitLabIssueAssignee(item.Assignee, currentUsername, currentUserID) {
		currentLabel = mergeLabelWithPriority(currentLabel, "Assigned", false)
	}

	if currentLabel == "Authored" || currentLabel == "Assigned" {
		return currentLabel, nil, nil
	}

	notes, err := listAllGitLabIssueNotes(ctx, client, projectID, item.IID)
	if err != nil {
		return "", nil, err
	}

	commented, mentioned := gitLabNotesInvolvement(notes, item.Description, currentUsername, currentUserID)
	if commented {
		currentLabel = mergeLabelWithPriority(currentLabel, "Commented", false)
	}
	if mentioned {
		currentLabel = mergeLabelWithPriority(currentLabel, "Mentioned", false)
	}

	if currentLabel == "" {
		return "Involved", notes, nil
	}
	return currentLabel, notes, nil
}

func persistGitLabNotes(db *Database, projectPath, itemType string, itemIID int, notes []*gitlab.Note) error {
	if db == nil || len(notes) == 0 {
		return nil
	}

	for _, note := range notes {
		if note == nil {
			continue
		}

		authorUsername := ""
		authorID := int64(0)
		author := note.Author
		authorUsername = strings.TrimSpace(author.Username)
		authorID = author.ID

		record := GitLabNoteRecord{
			ProjectPath:    projectPath,
			ItemType:       itemType,
			ItemIID:        itemIID,
			NoteID:         int64(note.ID),
			Body:           note.Body,
			AuthorUsername: authorUsername,
			AuthorID:       authorID,
		}

		if err := db.SaveGitLabNote(record, config.debugMode); err != nil {
			return err
		}
	}

	return nil
}

func loadGitLabCachedActivities(cutoff time.Time) ([]PRActivity, []IssueActivity, error) {
	if config.db == nil {
		return []PRActivity{}, []IssueActivity{}, nil
	}

	allMRs, mrLabels, err := config.db.GetAllGitLabMergeRequestsWithLabels(config.debugMode)
	if err != nil {
		return nil, nil, err
	}

	activities := make([]PRActivity, 0, len(allMRs))
	for key, mr := range allMRs {
		if mr.UpdatedAt.IsZero() || mr.UpdatedAt.Before(cutoff) {
			continue
		}

		projectPath, ok := parseGitLabMRProjectPath(key)
		if !ok || !isGitLabProjectAllowed(projectPath) {
			continue
		}

		activities = append(activities, PRActivity{
			Label:     mrLabels[key],
			Owner:     projectPath,
			Repo:      "",
			MR:        mr,
			UpdatedAt: mr.UpdatedAt,
		})
	}

	allIssues, issueLabels, err := config.db.GetAllGitLabIssuesWithLabels(config.debugMode)
	if err != nil {
		return nil, nil, err
	}

	issueActivities := make([]IssueActivity, 0, len(allIssues))
	for key, issue := range allIssues {
		if issue.UpdatedAt.IsZero() || issue.UpdatedAt.Before(cutoff) {
			continue
		}

		projectPath, ok := parseGitLabIssueProjectPath(key)
		if !ok || !isGitLabProjectAllowed(projectPath) {
			continue
		}

		issueActivities = append(issueActivities, IssueActivity{
			Label:     issueLabels[key],
			Owner:     projectPath,
			Repo:      "",
			Issue:     issue,
			UpdatedAt: issue.UpdatedAt,
		})
	}

	activities, issueActivities, err = linkGitLabCrossReferencesOffline(config.db, activities, issueActivities)
	if err != nil {
		return nil, nil, err
	}

	return activities, issueActivities, nil
}

var (
	gitLabIssueSameProjectRefPattern = regexp.MustCompile(`(?i)(?:^|[^a-z0-9_])#([0-9]+)\b`)
	gitLabIssueQualifiedRefPattern   = regexp.MustCompile(`(?i)([a-z0-9_.-]+(?:/[a-z0-9_.-]+)+)#([0-9]+)\b`)
	gitLabIssueURLRefPattern         = regexp.MustCompile(`(?i)https?://[^\s]+/([a-z0-9_.-]+(?:/[a-z0-9_.-]+)+)/-/issues/([0-9]+)\b`)
	gitLabIssueRelativeURLRefPattern = regexp.MustCompile(`(?i)/-/issues/([0-9]+)\b`)
)

func linkGitLabCrossReferencesOnline(
	ctx context.Context,
	client *gitlab.Client,
	activities []PRActivity,
	issueActivities []IssueActivity,
	projectIDByPath map[string]int64,
	mrNotesByKey map[string][]*gitlab.Note,
	db *Database,
) ([]PRActivity, []IssueActivity, error) {
	mrToIssueKeys := make(map[string]map[string]struct{}, len(activities))

	for _, activity := range activities {
		projectPath := normalizeProjectPathWithNamespace(activity.Owner)
		projectID, ok := projectIDByPath[projectPath]
		if !ok {
			continue
		}

		mrKey := buildGitLabMergeRequestKey(projectPath, activity.MR.Number)
		closedIssues, err := listGitLabIssuesClosedOnMergeRequest(ctx, client, projectID, int64(activity.MR.Number))
		if err == nil {
			resolvedKeys := make(map[string]struct{})
			for _, item := range closedIssues {
				issueKey, ok := gitLabIssueKeyFromIssue(item, projectPath)
				if !ok {
					continue
				}
				resolvedKeys[issueKey] = struct{}{}
			}
			if len(resolvedKeys) > 0 {
				mrToIssueKeys[mrKey] = resolvedKeys
			}
			continue
		}

		fallbackKeys := gitLabIssueReferenceKeysFromText(activity.MR.Body, projectPath)
		if len(fallbackKeys) == 0 {
			notes := mrNotesByKey[mrKey]
			if len(notes) == 0 {
				notes, err = listAllGitLabMergeRequestNotes(ctx, client, projectID, int64(activity.MR.Number))
				if err == nil {
					mrNotesByKey[mrKey] = notes
					if db != nil {
						if persistErr := persistGitLabNotes(db, projectPath, "mr", activity.MR.Number, notes); persistErr != nil {
							config.dbErrorCount.Add(1)
							if config.debugMode {
								fmt.Printf("  [DB] Warning: Failed to save GitLab MR notes %s!%d: %v\n", projectPath, activity.MR.Number, persistErr)
							}
						}
					}
				}
			}

			for _, note := range notes {
				if note == nil {
					continue
				}
				for issueKey := range gitLabIssueReferenceKeysFromText(note.Body, projectPath) {
					fallbackKeys[issueKey] = struct{}{}
				}
			}
		}

		if len(fallbackKeys) > 0 {
			mrToIssueKeys[mrKey] = fallbackKeys
		}
	}

	nestedActivities := nestGitLabIssues(activities, issueActivities, mrToIssueKeys)
	return nestedActivities, filterStandaloneGitLabIssues(nestedActivities, issueActivities), nil
}

func linkGitLabCrossReferencesOffline(db *Database, activities []PRActivity, issueActivities []IssueActivity) ([]PRActivity, []IssueActivity, error) {
	mrToIssueKeys := make(map[string]map[string]struct{}, len(activities))

	for _, activity := range activities {
		projectPath := normalizeProjectPathWithNamespace(activity.Owner)
		mrKey := buildGitLabMergeRequestKey(projectPath, activity.MR.Number)
		linked := gitLabIssueReferenceKeysFromText(activity.MR.Body, projectPath)
		if len(linked) == 0 && db != nil {
			notes, err := db.GetGitLabNotes(projectPath, "mr", activity.MR.Number)
			if err != nil {
				return nil, nil, err
			}
			for _, note := range notes {
				for issueKey := range gitLabIssueReferenceKeysFromText(note.Body, projectPath) {
					linked[issueKey] = struct{}{}
				}
			}
		}

		if len(linked) > 0 {
			mrToIssueKeys[mrKey] = linked
		}
	}

	nestedActivities := nestGitLabIssues(activities, issueActivities, mrToIssueKeys)
	return nestedActivities, filterStandaloneGitLabIssues(nestedActivities, issueActivities), nil
}

func listGitLabIssuesClosedOnMergeRequest(ctx context.Context, client *gitlab.Client, projectID int64, mergeRequestIID int64) ([]*gitlab.Issue, error) {
	allIssues := make([]*gitlab.Issue, 0)
	opts := &gitlab.GetIssuesClosedOnMergeOptions{ListOptions: gitlab.ListOptions{PerPage: 100, Page: 1}}

	for {
		issues, resp, err := client.MergeRequests.GetIssuesClosedOnMerge(projectID, mergeRequestIID, opts, gitlab.WithContext(ctx))
		if err != nil {
			return nil, err
		}
		allIssues = append(allIssues, issues...)
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	return allIssues, nil
}

func nestGitLabIssues(activities []PRActivity, issueActivities []IssueActivity, mrToIssueKeys map[string]map[string]struct{}) []PRActivity {
	issueByKey := make(map[string]IssueActivity, len(issueActivities))
	for _, issue := range issueActivities {
		issueByKey[buildGitLabIssueKey(issue.Owner, issue.Issue.Number)] = issue
	}

	for i := range activities {
		activities[i].Issues = nil
		mrKey := buildGitLabMergeRequestKey(activities[i].Owner, activities[i].MR.Number)
		linkedKeys := mrToIssueKeys[mrKey]
		if len(linkedKeys) == 0 {
			continue
		}
		for issueKey := range linkedKeys {
			issue, ok := issueByKey[issueKey]
			if !ok {
				continue
			}
			activities[i].Issues = append(activities[i].Issues, issue)
		}
		sort.Slice(activities[i].Issues, func(a, b int) bool {
			return activities[i].Issues[a].UpdatedAt.After(activities[i].Issues[b].UpdatedAt)
		})
	}

	return activities
}

func filterStandaloneGitLabIssues(activities []PRActivity, issueActivities []IssueActivity) []IssueActivity {
	linkedIssueKeys := make(map[string]struct{})
	for _, activity := range activities {
		for _, issue := range activity.Issues {
			linkedIssueKeys[buildGitLabIssueKey(issue.Owner, issue.Issue.Number)] = struct{}{}
		}
	}

	standalone := make([]IssueActivity, 0, len(issueActivities))
	for _, issue := range issueActivities {
		issueKey := buildGitLabIssueKey(issue.Owner, issue.Issue.Number)
		if _, linked := linkedIssueKeys[issueKey]; linked {
			continue
		}
		standalone = append(standalone, issue)
	}

	return standalone
}

func gitLabIssueReferenceKeysFromText(text, defaultProjectPath string) map[string]struct{} {
	results := make(map[string]struct{})
	if strings.TrimSpace(text) == "" {
		return results
	}

	for _, match := range gitLabIssueURLRefPattern.FindAllStringSubmatch(text, -1) {
		if len(match) < 3 {
			continue
		}
		iid, ok := parsePositiveInt(match[2])
		if !ok {
			continue
		}
		results[buildGitLabIssueKey(match[1], iid)] = struct{}{}
	}

	for _, match := range gitLabIssueQualifiedRefPattern.FindAllStringSubmatch(text, -1) {
		if len(match) < 3 {
			continue
		}
		iid, ok := parsePositiveInt(match[2])
		if !ok {
			continue
		}
		results[buildGitLabIssueKey(match[1], iid)] = struct{}{}
	}

	defaultProjectPath = normalizeProjectPathWithNamespace(defaultProjectPath)
	if defaultProjectPath != "" {
		for _, match := range gitLabIssueRelativeURLRefPattern.FindAllStringSubmatch(text, -1) {
			if len(match) < 2 {
				continue
			}
			iid, ok := parsePositiveInt(match[1])
			if !ok {
				continue
			}
			results[buildGitLabIssueKey(defaultProjectPath, iid)] = struct{}{}
		}

		for _, match := range gitLabIssueSameProjectRefPattern.FindAllStringSubmatch(text, -1) {
			if len(match) < 2 {
				continue
			}
			iid, ok := parsePositiveInt(match[1])
			if !ok {
				continue
			}
			results[buildGitLabIssueKey(defaultProjectPath, iid)] = struct{}{}
		}
	}

	return results
}

func gitLabIssueKeyFromIssue(item *gitlab.Issue, defaultProjectPath string) (string, bool) {
	if item == nil || item.IID <= 0 {
		return "", false
	}

	if item.References != nil {
		if projectPath, iid, ok := parseGitLabQualifiedReference(item.References.Full); ok {
			return buildGitLabIssueKey(projectPath, iid), true
		}
	}

	defaultProjectPath = normalizeProjectPathWithNamespace(defaultProjectPath)
	if defaultProjectPath == "" {
		return "", false
	}
	return buildGitLabIssueKey(defaultProjectPath, int(item.IID)), true
}

func parseGitLabQualifiedReference(reference string) (string, int, bool) {
	for _, match := range gitLabIssueQualifiedRefPattern.FindAllStringSubmatch(reference, -1) {
		if len(match) < 3 {
			continue
		}
		iid, ok := parsePositiveInt(match[2])
		if !ok {
			continue
		}
		return normalizeProjectPathWithNamespace(match[1]), iid, true
	}
	return "", 0, false
}

func parsePositiveInt(raw string) (int, bool) {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		return 0, false
	}
	return value, true
}

func parseGitLabMRProjectPath(key string) (string, bool) {
	idx := strings.LastIndex(key, "#!")
	if idx <= 0 {
		return "", false
	}
	return key[:idx], true
}

func parseGitLabIssueProjectPath(key string) (string, bool) {
	idx := strings.LastIndex(key, "##")
	if idx <= 0 {
		return "", false
	}
	return key[:idx], true
}

func isGitLabProjectAllowed(projectPath string) bool {
	if config.allowedRepos == nil || len(config.allowedRepos) == 0 {
		return true
	}

	normalized := normalizeProjectPathWithNamespace(projectPath)
	for repo := range config.allowedRepos {
		if strings.EqualFold(normalizeProjectPathWithNamespace(repo), normalized) {
			return true
		}
	}

	return false
}

func needsLowerPriorityPRChecks(currentLabel string) bool {
	return shouldUpdateLabel(currentLabel, "Commented", true) || shouldUpdateLabel(currentLabel, "Mentioned", true)
}

func mergeLabelWithPriority(currentLabel, candidateLabel string, isPR bool) string {
	if shouldUpdateLabel(currentLabel, candidateLabel, isPR) {
		return candidateLabel
	}
	return currentLabel
}

func listAllGitLabMergeRequestNotes(ctx context.Context, client *gitlab.Client, projectID int64, mrIID int64) ([]*gitlab.Note, error) {
	allNotes := make([]*gitlab.Note, 0)
	options := &gitlab.ListMergeRequestNotesOptions{
		ListOptions: gitlab.ListOptions{PerPage: 100, Page: 1},
	}

	for {
		var (
			notes    []*gitlab.Note
			response *gitlab.Response
		)
		err := retryWithBackoff(func() error {
			var apiErr error
			notes, response, apiErr = client.Notes.ListMergeRequestNotes(projectID, mrIID, options, gitlab.WithContext(ctx))
			return apiErr
		}, fmt.Sprintf("GitLabListMergeRequestNotes %d!%d page %d", projectID, mrIID, options.Page))
		if err != nil {
			return nil, err
		}
		allNotes = append(allNotes, notes...)

		if response == nil || response.NextPage == 0 {
			break
		}
		options.Page = response.NextPage
	}

	return allNotes, nil
}

func listAllGitLabIssueNotes(ctx context.Context, client *gitlab.Client, projectID int64, issueIID int64) ([]*gitlab.Note, error) {
	allNotes := make([]*gitlab.Note, 0)
	options := &gitlab.ListIssueNotesOptions{
		ListOptions: gitlab.ListOptions{PerPage: 100, Page: 1},
	}

	for {
		var (
			notes    []*gitlab.Note
			response *gitlab.Response
		)
		err := retryWithBackoff(func() error {
			var apiErr error
			notes, response, apiErr = client.Notes.ListIssueNotes(projectID, issueIID, options, gitlab.WithContext(ctx))
			return apiErr
		}, fmt.Sprintf("GitLabListIssueNotes %d#%d page %d", projectID, issueIID, options.Page))
		if err != nil {
			return nil, err
		}
		allNotes = append(allNotes, notes...)

		if response == nil || response.NextPage == 0 {
			break
		}
		options.Page = response.NextPage
	}

	return allNotes, nil
}

func gitLabNotesInvolvement(notes []*gitlab.Note, description, currentUsername string, currentUserID int64) (bool, bool) {
	commented := false
	mentioned := containsGitLabUserMention(description, currentUsername)

	for _, note := range notes {
		if note == nil {
			continue
		}
		if matchesGitLabNoteAuthor(note.Author, currentUsername, currentUserID) {
			commented = true
		}
		if !mentioned && containsGitLabUserMention(note.Body, currentUsername) {
			mentioned = true
		}
		if commented && mentioned {
			break
		}
	}

	return commented, mentioned
}

func containsGitLabUserMention(text, username string) bool {
	if text == "" || username == "" {
		return false
	}
	needle := "@" + strings.ToLower(strings.TrimSpace(username))
	if needle == "@" {
		return false
	}
	return strings.Contains(strings.ToLower(text), needle)
}

func matchesGitLabNoteAuthor(author gitlab.NoteAuthor, username string, userID int64) bool {
	if userID > 0 && author.ID == userID {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(author.Username), strings.TrimSpace(username))
}

func matchesGitLabBasicUser(user *gitlab.BasicUser, username string, userID int64) bool {
	if user == nil {
		return false
	}
	if userID > 0 && user.ID == userID {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(user.Username), strings.TrimSpace(username))
}

func matchesGitLabIssueAuthor(author *gitlab.IssueAuthor, username string, userID int64) bool {
	if author == nil {
		return false
	}
	if userID > 0 && author.ID == userID {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(author.Username), strings.TrimSpace(username))
}

func matchesGitLabIssueAssignee(assignee *gitlab.IssueAssignee, username string, userID int64) bool {
	if assignee == nil {
		return false
	}
	if userID > 0 && assignee.ID == userID {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(assignee.Username), strings.TrimSpace(username))
}

func gitLabIssueAssigneeListContains(assignees []*gitlab.IssueAssignee, username string, userID int64) bool {
	for _, assignee := range assignees {
		if matchesGitLabIssueAssignee(assignee, username, userID) {
			return true
		}
	}
	return false
}

func gitLabBasicUserListContains(users []*gitlab.BasicUser, username string, userID int64) bool {
	for _, user := range users {
		if matchesGitLabBasicUser(user, username, userID) {
			return true
		}
	}
	return false
}

func gitLabApprovalStateReviewedByCurrentUser(state *gitlab.MergeRequestApprovalState, username string, userID int64) bool {
	if state == nil {
		return false
	}
	for _, rule := range state.Rules {
		if rule == nil {
			continue
		}
		if gitLabBasicUserListContains(rule.ApprovedBy, username, userID) {
			return true
		}
	}
	return false
}

func resolveAllowedGitLabProjects(ctx context.Context, client *gitlab.Client, allowedRepos map[string]bool) ([]gitLabProject, error) {
	if client == nil {
		return nil, fmt.Errorf("gitlab client is not configured")
	}

	if len(allowedRepos) == 0 {
		return []gitLabProject{}, nil
	}

	repoPaths := make([]string, 0, len(allowedRepos))
	for repo := range allowedRepos {
		normalized := normalizeProjectPathWithNamespace(repo)
		if normalized != "" {
			repoPaths = append(repoPaths, normalized)
		}
	}
	sort.Strings(repoPaths)

	projectIDCache := make(map[string]int64, len(repoPaths))
	projects := make([]gitLabProject, 0, len(repoPaths))
	for _, pathWithNamespace := range repoPaths {
		if id, ok := projectIDCache[pathWithNamespace]; ok {
			projects = append(projects, gitLabProject{PathWithNamespace: pathWithNamespace, ID: id})
			continue
		}

		var project *gitlab.Project
		err := retryWithBackoff(func() error {
			var apiErr error
			project, _, apiErr = client.Projects.GetProject(pathWithNamespace, nil, gitlab.WithContext(ctx))
			return apiErr
		}, fmt.Sprintf("GitLabGetProject %s", pathWithNamespace))
		if err != nil {
			return nil, fmt.Errorf("resolve project %s: %w", pathWithNamespace, err)
		}

		projectIDCache[pathWithNamespace] = project.ID
		projects = append(projects, gitLabProject{PathWithNamespace: pathWithNamespace, ID: project.ID})
	}

	return projects, nil
}

func listGitLabProjectMergeRequests(ctx context.Context, client *gitlab.Client, projectID int64, cutoff time.Time) ([]*gitlab.BasicMergeRequest, error) {
	allItems := make([]*gitlab.BasicMergeRequest, 0)
	options := &gitlab.ListProjectMergeRequestsOptions{
		ListOptions:  gitlab.ListOptions{PerPage: 100, Page: 1},
		State:        gitlab.Ptr("all"),
		UpdatedAfter: &cutoff,
	}

	for {
		var (
			items    []*gitlab.BasicMergeRequest
			response *gitlab.Response
		)
		err := retryWithBackoff(func() error {
			var apiErr error
			items, response, apiErr = client.MergeRequests.ListProjectMergeRequests(projectID, options, gitlab.WithContext(ctx))
			return apiErr
		}, fmt.Sprintf("GitLabListProjectMergeRequests %d page %d", projectID, options.Page))
		if err != nil {
			return nil, err
		}
		allItems = append(allItems, items...)

		if response == nil || response.NextPage == 0 {
			break
		}
		options.Page = response.NextPage
	}

	return allItems, nil
}

func listGitLabProjectIssues(ctx context.Context, client *gitlab.Client, projectID int64, cutoff time.Time) ([]*gitlab.Issue, error) {
	allItems := make([]*gitlab.Issue, 0)
	options := &gitlab.ListProjectIssuesOptions{
		ListOptions:  gitlab.ListOptions{PerPage: 100, Page: 1},
		State:        gitlab.Ptr("all"),
		UpdatedAfter: &cutoff,
	}

	for {
		var (
			items    []*gitlab.Issue
			response *gitlab.Response
		)
		err := retryWithBackoff(func() error {
			var apiErr error
			items, response, apiErr = client.Issues.ListProjectIssues(projectID, options, gitlab.WithContext(ctx))
			return apiErr
		}, fmt.Sprintf("GitLabListProjectIssues %d page %d", projectID, options.Page))
		if err != nil {
			return nil, err
		}
		allItems = append(allItems, items...)

		if response == nil || response.NextPage == 0 {
			break
		}
		options.Page = response.NextPage
	}

	return allItems, nil
}

func normalizeProjectPathWithNamespace(repo string) string {
	trimmed := strings.TrimSpace(repo)
	return strings.Trim(trimmed, "/")
}

func buildGitLabDedupKey(projectPath, itemType string, iid int64) string {
	return fmt.Sprintf("%s|%s|%d", strings.ToLower(projectPath), itemType, iid)
}

func toMergeRequestModelFromGitLab(item *gitlab.BasicMergeRequest) MergeRequestModel {
	if item == nil {
		return MergeRequestModel{}
	}

	state := strings.ToLower(item.State)
	merged := state == "merged" || item.MergedAt != nil
	normalizedState := "open"
	if merged || state == "closed" {
		normalizedState = "closed"
	}

	updatedAt := time.Time{}
	if item.UpdatedAt != nil {
		updatedAt = *item.UpdatedAt
	}

	userLogin := ""
	if item.Author != nil {
		userLogin = item.Author.Username
	}

	return MergeRequestModel{
		Number:    int(item.IID),
		Title:     item.Title,
		Body:      item.Description,
		State:     normalizedState,
		UpdatedAt: updatedAt,
		WebURL:    item.WebURL,
		UserLogin: userLogin,
		Merged:    merged,
	}
}

func toIssueModelFromGitLab(item *gitlab.Issue) IssueModel {
	if item == nil {
		return IssueModel{}
	}

	state := strings.ToLower(item.State)
	normalizedState := "open"
	if state == "closed" {
		normalizedState = "closed"
	}

	updatedAt := time.Time{}
	if item.UpdatedAt != nil {
		updatedAt = *item.UpdatedAt
	}

	userLogin := ""
	if item.Author != nil {
		userLogin = item.Author.Username
	}

	return IssueModel{
		Number:    int(item.IID),
		Title:     item.Title,
		Body:      item.Description,
		State:     normalizedState,
		UpdatedAt: updatedAt,
		WebURL:    item.WebURL,
		UserLogin: userLogin,
	}
}
