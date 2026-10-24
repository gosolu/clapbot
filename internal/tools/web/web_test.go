package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gosolu/clapbot/internal/config"
)

func TestHTMLToMarkdown(t *testing.T) {
	raw := `<html><head><title>Ignored</title><script>alert(1)</script></head><body><h1>Hello World</h1><p>Visit <a href="https://example.com">Example</a></p><ul><li>Item 1</li><li>Item 2</li></ul></body></html>`
	md := HTMLToMarkdown(raw)
	if !strings.Contains(md, "# Hello World") {
		t.Errorf("missing heading in %q", md)
	}
	if !strings.Contains(md, "[Example](https://example.com)") {
		t.Errorf("missing link in %q", md)
	}
	if !strings.Contains(md, "- Item 1") {
		t.Errorf("missing list item in %q", md)
	}
}

func TestDeepSeekSearchAndFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sk-ds-test" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
			return
		}
		switch r.URL.Path {
		case "/v1/tools/search":
			var req struct {
				Query string `json:"query"`
				Count int    `json:"count"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode search req: %v", err)
			}
			if req.Query != "DeepSeek V4 release" || req.Count != 5 {
				t.Errorf("unexpected search req: %+v", req)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"results": [
					{
						"rerank_score": 2.4,
						"title": "DeepSeek V4 model released",
						"url": "https://example.com/deepseek-v4",
						"snippet": "Short snippet",
						"summary": "DeepSeek has released the V4 model with tool calling support."
					}
				]
			}`))
		case "/v1/tools/fetch":
			var req struct {
				URL string `json:"url"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode fetch req: %v", err)
			}
			if req.URL != "https://example.com/article" {
				t.Errorf("unexpected fetch req: %+v", req)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"title": "Example article",
				"content": "# Example article\n\nArticle content."
			}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	oldBase := deepseekBaseURL
	deepseekBaseURL = srv.URL
	defer func() { deepseekBaseURL = oldBase }()

	cfg := config.WebConfig{
		SearchProvider: "deepseek",
		SearchAPIKey:   "sk-ds-test",
	}

	searchTool := NewSearchTool(WithConfig(cfg), WithHTTPClient(srv.Client()))
	searchRes, err := searchTool.Call(context.Background(), json.RawMessage(`{"query":"DeepSeek V4 release","max_results":5}`))
	if err != nil {
		t.Fatalf("searchTool.Call failed: %v", err)
	}
	searchOut := searchRes.Text()
	if !strings.Contains(searchOut, "DeepSeek V4 model released") || !strings.Contains(searchOut, "https://example.com/deepseek-v4") || !strings.Contains(searchOut, "DeepSeek has released the V4 model with tool calling support.") {
		t.Errorf("unexpected search output: %s", searchOut)
	}

	fetchTool := NewFetchTool(WithConfig(cfg), WithHTTPClient(srv.Client()))
	fetchRes, err := fetchTool.Call(context.Background(), json.RawMessage(`{"url":"https://example.com/article"}`))
	if err != nil {
		t.Fatalf("fetchTool.Call failed: %v", err)
	}
	fetchOut := fetchRes.Text()
	if !strings.Contains(fetchOut, "# Example article") || !strings.Contains(fetchOut, "Article content.") {
		t.Errorf("unexpected fetch output: %s", fetchOut)
	}
}

