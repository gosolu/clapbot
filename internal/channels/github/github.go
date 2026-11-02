package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gosolu/clapbot/internal/config"
	"github.com/gosolu/clapbot/internal/runtime"
	"github.com/gosolu/clapbot/internal/superagent"
	"github.com/gosolu/httpr"
)

// Bot implements the GitHub repository watcher and comment channel adapter.
type Bot struct {
	cfg     config.GitHubConfig
	engine  *runtime.Engine
	apiBase string
	token   string
	owner   string
	repo    string

	mu        sync.Mutex
	cancel    context.CancelFunc
	lastPoll  time.Time
	doRequest func(ctx context.Context, req *http.Request) (*http.Response, error)
}

var _ runtime.ChannelAdapter = (*Bot)(nil)

// New creates a new GitHub channel adapter.
func New(cfg config.GitHubConfig, eng *runtime.Engine) *Bot {
	token := strings.TrimSpace(cfg.Token)
	if token == "" {
		token = strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
	}
	if token == "" {
		token = strings.TrimSpace(os.Getenv("GH_TOKEN"))
	}

	parts := strings.Split(strings.TrimSpace(cfg.Repository), "/")
	owner := ""
	repo := ""
	if len(parts) == 2 {
		owner = parts[0]
		repo = parts[1]
	}

	return &Bot{
		cfg:       cfg,
		engine:    eng,
		apiBase:   "https://api.github.com",
		token:     token,
		owner:     owner,
		repo:      repo,
		lastPoll:  time.Now().Add(-10 * time.Minute),
		doRequest: httpr.Do,
	}
}

// Name implements runtime.ChannelAdapter.
func (b *Bot) Name() string { return "github" }

// Start implements runtime.ChannelAdapter.
func (b *Bot) Start(ctx context.Context) error {
	if b.owner == "" || b.repo == "" {
		return fmt.Errorf("github: repository must be in owner/repo format: %q", b.cfg.Repository)
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	pollCtx, cancel := context.WithCancel(ctx)
	b.cancel = cancel

	go b.pollLoop(pollCtx)
	slog.InfoContext(ctx, "github source started", "repo", b.cfg.Repository, "agent", b.cfg.AgentAccount)
	return nil
}

// Stop implements runtime.ChannelAdapter.
func (b *Bot) Stop() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cancel != nil {
		b.cancel()
		b.cancel = nil
	}
	return nil
}

// SendText posts a comment on an Issue or PR.
func (b *Bot) SendText(ctx context.Context, target, text string) error {
	repoFullName, number, err := b.parseTarget(target)
	if err != nil {
		return err
	}

	parts := strings.Split(repoFullName, "/")
	if len(parts) != 2 {
		return fmt.Errorf("invalid repo full name %q", repoFullName)
	}
	owner, repo := parts[0], parts[1]

	endpoint := fmt.Sprintf("%s/repos/%s/%s/issues/%d/comments", b.apiBase, owner, repo, number)
	bodyMap := map[string]string{"body": text}
	payload, _ := json.Marshal(bodyMap)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	b.setHeaders(req)

	resp, err := b.doRequest(ctx, req)
	if err != nil {
		return fmt.Errorf("github post comment: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		out, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("github post comment status %d: %s", resp.StatusCode, string(out))
	}

	slog.InfoContext(ctx, "github comment posted", "repo", repoFullName, "number", number)
	return nil
}

// SendFile posts a comment referencing the file path.
func (b *Bot) SendFile(ctx context.Context, target, filePath, caption string) error {
	msg := fmt.Sprintf("[File: %s]\n\n%s", filePath, caption)
	return b.SendText(ctx, target, msg)
}

func (b *Bot) parseTarget(target string) (repoFullName string, number int, err error) {
	target = strings.TrimSpace(target)
	if idx := strings.LastIndexByte(target, ':'); idx > 0 {
		repoFullName = target[:idx]
		number, err = strconv.Atoi(target[idx+1:])
		return
	}
	if b.cfg.Repository != "" {
		repoFullName = b.cfg.Repository
		number, err = strconv.Atoi(target)
		return
	}
	return "", 0, fmt.Errorf("invalid github target format %q (expected owner/repo:number)", target)
}

func (b *Bot) setHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("Content-Type", "application/json")
	if b.token != "" {
		req.Header.Set("Authorization", "Bearer "+b.token)
	}
}

// =============================================================================
// Polling Loop
// =============================================================================

type ghUser struct {
	Login string `json:"login"`
	Type  string `json:"type,omitempty"`
}

type ghIssue struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	User      ghUser    `json:"user"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	ClosedAt  *time.Time `json:"closed_at,omitempty"`
	HTMLURL   string    `json:"html_url"`
	PullReq   *struct {
		URL string `json:"url"`
	} `json:"pull_request,omitempty"`
}

type ghComment struct {
	ID        int64     `json:"id"`
	Body      string    `json:"body"`
	User      ghUser    `json:"user"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	HTMLURL   string    `json:"html_url"`
	IssueURL  string    `json:"issue_url"`
}

type ghPull struct {
	Number int  `json:"number"`
	Merged bool `json:"merged"`
}

func (b *Bot) pollLoop(ctx context.Context) {
	interval := time.Duration(b.cfg.PollSeconds) * time.Second
	if interval < 10*time.Second {
		interval = 60 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Initial immediate poll
	b.pollOnce(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.pollOnce(ctx)
		}
	}
}

func (b *Bot) pollOnce(ctx context.Context) {
	if b.engine.SuperAgent() == nil {
		return
	}

	now := time.Now()
	since := b.lastPoll
	b.lastPoll = now

	_ = b.pollIssues(ctx, since)
	_ = b.pollComments(ctx, since)
}

func (b *Bot) pollIssues(ctx context.Context, since time.Time) error {
	endpoint := fmt.Sprintf("%s/repos/%s/%s/issues?since=%s&state=all&per_page=30",
		b.apiBase, b.owner, b.repo, since.UTC().Format(time.RFC3339))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	b.setHeaders(req)

	resp, err := b.doRequest(ctx, req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("github list issues status: %d", resp.StatusCode)
	}

	var issues []ghIssue
	if err := json.NewDecoder(resp.Body).Decode(&issues); err != nil {
		return err
	}

	agent := strings.ToLower(strings.TrimSpace(b.cfg.AgentAccount))

	for _, is := range issues {
		// Drop bot's own actions
		if agent != "" && strings.EqualFold(is.User.Login, agent) {
			continue
		}

		threadKey := fmt.Sprintf("gh:%s/%s:%d", b.owner, b.repo, is.Number)

		// Check PR close / merge
		if is.PullReq != nil && strings.EqualFold(is.State, "closed") {
			merged := false
			if pr, err := b.fetchPull(ctx, is.Number); err == nil && pr != nil {
				merged = pr.Merged
			}
			outcome := "closed"
			if merged {
				outcome = "merged"
			}
			at := nowOr(is.ClosedAt)
			_ = b.engine.SuperAgent().ThreadClosed(ctx, threadKey, outcome, at)
			continue
		}

		// Determine addressed
		addressed := superagent.AddressNone
		if agent != "" && strings.Contains(strings.ToLower(is.Body), "@"+agent) {
			addressed = superagent.AddressMention
		} else if !b.cfg.ObserveIssues {
			continue
		}

		actor := superagent.Actor{
			ID:   is.User.Login,
			Name: is.User.Login,
			Bot:  strings.HasSuffix(strings.ToLower(is.User.Login), "[bot]"),
		}

		tr := superagent.Trigger{
			ID:        fmt.Sprintf("github:issue:%s/%s:%d:%s", b.owner, b.repo, is.Number, is.UpdatedAt.UTC().Format(time.RFC3339)),
			Source:    "github",
			Thread:    threadKey,
			Scope:     "issues",
			Actor:     actor,
			Addressed: addressed,
			Text:      fmt.Sprintf("#%d: %s\n\n%s", is.Number, is.Title, is.Body),
			Channel: superagent.ChannelAddress{
				Source: "github",
				ChatID: fmt.Sprintf("%s/%s:%d", b.owner, b.repo, is.Number),
			},
			At: is.UpdatedAt,
		}

		res, err := b.engine.SuperAgent().Receive(ctx, tr)
		if err != nil {
			slog.ErrorContext(ctx, "github issue receive error", "trigger_id", tr.ID, "error", err)
		} else {
			slog.InfoContext(ctx, "github issue received", "trigger_id", tr.ID, "disposition", string(res))
		}
	}
	return nil
}

func (b *Bot) pollComments(ctx context.Context, since time.Time) error {
	endpoint := fmt.Sprintf("%s/repos/%s/%s/issues/comments?since=%s&per_page=30",
		b.apiBase, b.owner, b.repo, since.UTC().Format(time.RFC3339))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	b.setHeaders(req)

	resp, err := b.doRequest(ctx, req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("github list comments status: %d", resp.StatusCode)
	}

	var comments []ghComment
	if err := json.NewDecoder(resp.Body).Decode(&comments); err != nil {
		return err
	}

	agent := strings.ToLower(strings.TrimSpace(b.cfg.AgentAccount))

	for _, cm := range comments {
		if agent != "" && strings.EqualFold(cm.User.Login, agent) {
			continue
		}

		issueNum := parseIssueNumber(cm.IssueURL)
		if issueNum <= 0 {
			continue
		}

		threadKey := fmt.Sprintf("gh:%s/%s:%d", b.owner, b.repo, issueNum)

		addressed := superagent.AddressNone
		if agent != "" && strings.Contains(strings.ToLower(cm.Body), "@"+agent) {
			addressed = superagent.AddressMention
		} else if !b.cfg.ObserveComments {
			continue
		}

		actor := superagent.Actor{
			ID:   cm.User.Login,
			Name: cm.User.Login,
			Bot:  strings.HasSuffix(strings.ToLower(cm.User.Login), "[bot]"),
		}

		tr := superagent.Trigger{
			ID:        fmt.Sprintf("github:comment:%s/%s:%d:%s", b.owner, b.repo, cm.ID, cm.UpdatedAt.UTC().Format(time.RFC3339)),
			Source:    "github",
			Thread:    threadKey,
			Scope:     "comments",
			Actor:     actor,
			Addressed: addressed,
			Text:      cm.Body,
			Channel: superagent.ChannelAddress{
				Source: "github",
				ChatID: fmt.Sprintf("%s/%s:%d", b.owner, b.repo, issueNum),
			},
			At: cm.UpdatedAt,
		}

		res, err := b.engine.SuperAgent().Receive(ctx, tr)
		if err != nil {
			slog.ErrorContext(ctx, "github comment receive error", "trigger_id", tr.ID, "error", err)
		} else {
			slog.InfoContext(ctx, "github comment received", "trigger_id", tr.ID, "disposition", string(res))
		}
	}
	return nil
}

func (b *Bot) fetchPull(ctx context.Context, number int) (*ghPull, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", b.apiBase, b.owner, b.repo, number)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	b.setHeaders(req)

	resp, err := b.doRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("fetch pull status: %d", resp.StatusCode)
	}

	var pr ghPull
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return nil, err
	}
	return &pr, nil
}

func parseIssueNumber(issueURL string) int {
	idx := strings.LastIndexByte(issueURL, '/')
	if idx < 0 || idx == len(issueURL)-1 {
		return 0
	}
	n, _ := strconv.Atoi(issueURL[idx+1:])
	return n
}

func nowOr(t *time.Time) time.Time {
	if t != nil {
		return *t
	}
	return time.Now()
}
