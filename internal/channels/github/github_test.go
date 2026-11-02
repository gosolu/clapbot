package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/gosolu/clapbot/internal/config"
	"github.com/gosolu/clapbot/internal/provider"
	"github.com/gosolu/clapbot/internal/runtime"
	"github.com/gosolu/clapbot/internal/store"
	"github.com/gosolu/clapbot/internal/superagent"
	"github.com/gosolu/react/provider/mock"
)

type mockRoundTripper func(req *http.Request) (*http.Response, error)

func (m mockRoundTripper) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	return m(req)
}

func jsonResponse(status int, v any) *http.Response {
	b, _ := json.Marshal(v)
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewReader(b)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
}

func TestGitHubChannelSendText(t *testing.T) {
	ctx := context.Background()
	var postedBody string
	var postedURL string

	mockDo := func(ctx context.Context, req *http.Request) (*http.Response, error) {
		postedURL = req.URL.String()
		var m map[string]string
		_ = json.NewDecoder(req.Body).Decode(&m)
		postedBody = m["body"]
		return jsonResponse(http.StatusCreated, map[string]any{"id": 123}), nil
	}

	bot := New(config.GitHubConfig{
		Repository:   "org/repo",
		AgentAccount: "clapbot[bot]",
		Token:        "ghp_secret",
	}, nil)
	bot.doRequest = mockDo

	err := bot.SendText(ctx, "org/repo:42", "Task completed via PR #43")
	if err != nil {
		t.Fatalf("SendText: %v", err)
	}

	expectedURL := "https://api.github.com/repos/org/repo/issues/42/comments"
	if postedURL != expectedURL {
		t.Fatalf("expected URL %s, got %s", expectedURL, postedURL)
	}
	if postedBody != "Task completed via PR #43" {
		t.Fatalf("unexpected body: %s", postedBody)
	}
}

func TestGitHubPollIssuesAndComments(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg, _ := config.Load(tmp)
	cfg.Mode = "superagent"
	cfg.SuperAgent.AcceptMode = "auto"
	cfg.Channels.GitHub = config.GitHubConfig{
		Enabled:         true,
		Repository:      "owner/testrepo",
		AgentAccount:    "clapbot",
		AllowedUsers:    []string{"alice"},
		ObserveIssues:   false,
		ObserveComments: false,
	}

	st, _ := store.Open(":memory:")
	defer func() { _ = st.Close() }()

	router, _ := provider.NewRouter(cfg)
	router.RegisterProfile("default", config.ProfileConfig{Provider: "mock", Model: "mock"}, mock.New(
		mock.Text("OK"),
	))

	eng, err := runtime.NewEngine(cfg, st, router)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	defer func() { _ = eng.Close() }()

	bot := New(cfg.Channels.GitHub, eng)

	now := time.Now()
	// Mock GitHub responses
	bot.doRequest = func(ctx context.Context, req *http.Request) (*http.Response, error) {
		url := req.URL.Path
		if url == "/repos/owner/testrepo/issues" {
			issues := []ghIssue{
				{
					Number:    10,
					Title:     "Add health check endpoint",
					Body:      "Hey @clapbot please add /healthz endpoint",
					User:      ghUser{Login: "alice"},
					State:     "open",
					CreatedAt: now,
					UpdatedAt: now,
				},
				{
					Number:    11,
					Title:     "Own issue",
					Body:      "I created this myself",
					User:      ghUser{Login: "clapbot"}, // should be skipped
					State:     "open",
					CreatedAt: now,
					UpdatedAt: now,
				},
			}
			return jsonResponse(http.StatusOK, issues), nil
		}
		if url == "/repos/owner/testrepo/issues/comments" {
			comments := []ghComment{
				{
					ID:        501,
					Body:      "Please update tests @clapbot",
					User:      ghUser{Login: "alice"},
					IssueURL:  "https://api.github.com/repos/owner/testrepo/issues/10",
					CreatedAt: now,
					UpdatedAt: now,
				},
			}
			return jsonResponse(http.StatusOK, comments), nil
		}
		return jsonResponse(http.StatusOK, map[string]any{}), nil
	}

	// Poll issues
	err = bot.pollIssues(ctx, now.Add(-1*time.Hour))
	if err != nil {
		t.Fatalf("pollIssues: %v", err)
	}

	// Verify trigger in superagent
	triggerID := fmt.Sprintf("github:issue:owner/testrepo:10:%s", now.UTC().Format(time.RFC3339))
	dec, err := eng.SuperAgent().Decide(ctx, triggerID, "accept", "Accepted", nil)
	if err != nil || dec == nil || dec.TicketID == "" {
		t.Fatalf("expected accepted ticket for github issue: %v, dec=%+v", err, dec)
	}

	// Poll comments
	err = bot.pollComments(ctx, now.Add(-1*time.Hour))
	if err != nil {
		t.Fatalf("pollComments: %v", err)
	}
}

func TestGitHubPollPullClose(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg, _ := config.Load(tmp)
	cfg.Mode = "superagent"
	cfg.Channels.GitHub = config.GitHubConfig{
		Enabled:      true,
		Repository:   "owner/testrepo",
		AgentAccount: "clapbot",
	}

	st, _ := store.Open(":memory:")
	defer func() { _ = st.Close() }()

	router, _ := provider.NewRouter(cfg)
	router.RegisterProfile("default", config.ProfileConfig{Provider: "mock", Model: "mock"}, mock.New())

	eng, _ := runtime.NewEngine(cfg, st, router)
	defer func() { _ = eng.Close() }()

	bot := New(cfg.Channels.GitHub, eng)

	closedAt := time.Now()
	bot.doRequest = func(ctx context.Context, req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/repos/owner/testrepo/issues" {
			issues := []ghIssue{
				{
					Number:   22,
					Title:    "Feature PR",
					State:    "closed",
					ClosedAt: &closedAt,
					PullReq:  &struct{ URL string `json:"url"` }{URL: "https://api.github.com/..."},
					User:     ghUser{Login: "alice"},
				},
			}
			return jsonResponse(http.StatusOK, issues), nil
		}
		if req.URL.Path == "/repos/owner/testrepo/pulls/22" {
			pr := ghPull{Number: 22, Merged: true}
			return jsonResponse(http.StatusOK, pr), nil
		}
		return jsonResponse(http.StatusOK, map[string]any{}), nil
	}

	// Create and bind ticket to this PR thread
	tr := superagent.Trigger{
		ID:        "t_orig_1",
		Source:    "telegram",
		Thread:    "tg:100",
		Actor:     superagent.Actor{ID: "alice"},
		Addressed: superagent.AddressDirect,
		Text:      "Open PR",
	}
	_, _ = eng.SuperAgent().Receive(ctx, tr)
	dec, _ := eng.SuperAgent().Decide(ctx, tr.ID, "accept", "Accept", nil)

	prThreadKey := "gh:owner/testrepo:22"
	_ = eng.SuperAgent().BindThread(ctx, dec.TicketID, prThreadKey, true)

	// Now poll issues: PR 22 was merged!
	err := bot.pollIssues(ctx, closedAt.Add(-1*time.Hour))
	if err != nil {
		t.Fatalf("pollIssues: %v", err)
	}

	// Ticket should be ended as completed with outcome "merged"!
	var ticket *superagent.Ticket
	_ = st.Transact(ctx, func(tx superagent.Tx) error {
		ticket, _ = tx.GetTicket(dec.TicketID)
		return nil
	})
	if ticket == nil || ticket.Status != superagent.TicketEnded || ticket.EndReason != "merged" {
		t.Fatalf("expected ticket ended with merged outcome, got: %+v", ticket)
	}
}
