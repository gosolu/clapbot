package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/gosolu/clapbot/internal/config"
	"github.com/gosolu/react"
)

type fetchInput struct {
	URL      string `json:"url" desc:"The HTTP/HTTPS URL to fetch and extract text/markdown from."`
	MaxChars int    `json:"max_chars,omitempty" desc:"Maximum characters to return (default 20000)."`
}

type searchInput struct {
	Query      string `json:"query" desc:"Search query string."`
	MaxResults int    `json:"max_results,omitempty" desc:"Maximum number of search results to return (default 6)."`
}

// SearchResult represents a single web search hit.
type SearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// Tools returns the web_fetch and web_search tools configured by WebConfig.
func Tools(cfg config.WebConfig, client *http.Client) []react.Tool {
	if client == nil {
		client = &http.Client{Timeout: 25 * time.Second}
	}
	return []react.Tool{
		NewFetchTool(client),
		NewSearchTool(cfg, client),
	}
}

// NewFetchTool builds the web_fetch tool.
func NewFetchTool(client *http.Client) react.Tool {
	if client == nil {
		client = &http.Client{Timeout: 25 * time.Second}
	}
	return react.NewTool(
		"web_fetch",
		"Fetch an HTTP/HTTPS URL and extract clean readable text/markdown content.",
		func(ctx context.Context, in fetchInput) (string, error) {
			u := strings.TrimSpace(in.URL)
			if u == "" {
				return "", fmt.Errorf("url is required")
			}
			if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
				u = "https://" + u
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
			if err != nil {
				return "", err
			}
			req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Clapbot/1.0)")
			req.Header.Set("Accept", "text/markdown, text/html, application/xhtml+xml, text/plain;q=0.9, */*;q=0.8")

			resp, err := client.Do(req)
			if err != nil {
				return "", fmt.Errorf("fetch %s: %w", u, err)
			}
			defer func() { _ = resp.Body.Close() }()

			body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
			if err != nil {
				return "", err
			}
			if resp.StatusCode >= 400 {
				return "", fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, u, truncateRunes(string(body), 500))
			}

			ct := strings.ToLower(resp.Header.Get("Content-Type"))
			var text string
			if strings.Contains(ct, "html") || bytes.Contains(body[:min(len(body), 512)], []byte("<html")) {
				text = HTMLToMarkdown(string(body))
			} else {
				text = string(body)
			}

			maxChars := in.MaxChars
			if maxChars <= 0 {
				maxChars = 20000
			}
			return truncateRunes(text, maxChars), nil
		},
		react.ReadOnly(),
	)
}

// NewSearchTool builds the web_search tool.
func NewSearchTool(cfg config.WebConfig, client *http.Client) react.Tool {
	if client == nil {
		client = &http.Client{Timeout: 25 * time.Second}
	}
	return react.NewTool(
		"web_search",
		"Search the web for up-to-date information and return titles, URLs, and snippets.",
		func(ctx context.Context, in searchInput) (string, error) {
			q := strings.TrimSpace(in.Query)
			if q == "" {
				return "", fmt.Errorf("query is required")
			}
			maxRes := in.MaxResults
			if maxRes <= 0 {
				maxRes = 6
			}
			if maxRes > 15 {
				maxRes = 15
			}

			var (
				results []SearchResult
				err     error
			)
			switch strings.ToLower(strings.TrimSpace(cfg.SearchProvider)) {
			case "brave":
				results, err = searchBrave(ctx, client, cfg.SearchAPIKey, q, maxRes)
			case "tavily":
				results, err = searchTavily(ctx, client, cfg.SearchAPIKey, q, maxRes)
			case "searxng":
				results, err = searchSearXNG(ctx, client, cfg.SearxngURL, q, maxRes)
			default:
				results, err = searchDuckDuckGo(ctx, client, q, maxRes)
			}
			if err != nil {
				return "", err
			}
			if len(results) == 0 {
				return "No search results found.", nil
			}
			var b strings.Builder
			for i, r := range results {
				fmt.Fprintf(&b, "%d. **%s**\n   URL: %s\n   %s\n\n", i+1, r.Title, r.URL, r.Snippet)
			}
			return strings.TrimSpace(b.String()), nil
		},
		react.ReadOnly(),
	)
}

var (
	reScriptStyle = regexp.MustCompile(`(?is)<(script|style|noscript|svg|head)[^>]*>.*?</(script|style|noscript|svg|head)>`)
	reHeading     = regexp.MustCompile(`(?is)<h([1-6])[^>]*>(.*?)</h[1-6]>`)
	reLink        = regexp.MustCompile(`(?is)<a[^>]+href=["']([^"']+)["'][^>]*>(.*?)</a>`)
	reListItem    = regexp.MustCompile(`(?is)<li[^>]*>(.*?)</li>`)
	reBlockEnd    = regexp.MustCompile(`(?is)</(p|div|section|article|tr|blockquote|pre|ul|ol|table)>|<br\s*/?>`)
	reAllTags     = regexp.MustCompile(`(?is)<[^>]+>`)
	reMultiBlank  = regexp.MustCompile(`\n{3,}`)
)

// HTMLToMarkdown converts raw HTML into readable Markdown-like text using stdlib regexes.
func HTMLToMarkdown(raw string) string {
	s := reScriptStyle.ReplaceAllString(raw, "")
	s = reHeading.ReplaceAllStringFunc(s, func(m string) string {
		sub := reHeading.FindStringSubmatch(m)
		if len(sub) < 3 {
			return m
		}
		level := int(sub[1][0] - '0')
		inner := strings.TrimSpace(stripTags(sub[2]))
		if inner == "" {
			return ""
		}
		return "\n\n" + strings.Repeat("#", level) + " " + inner + "\n\n"
	})
	s = reLink.ReplaceAllStringFunc(s, func(m string) string {
		sub := reLink.FindStringSubmatch(m)
		if len(sub) < 3 {
			return m
		}
		href := strings.TrimSpace(sub[1])
		label := strings.TrimSpace(stripTags(sub[2]))
		if label == "" || strings.HasPrefix(href, "javascript:") {
			return label
		}
		return fmt.Sprintf("[%s](%s)", label, href)
	})
	s = reListItem.ReplaceAllStringFunc(s, func(m string) string {
		sub := reListItem.FindStringSubmatch(m)
		if len(sub) < 2 {
			return m
		}
		return "\n- " + strings.TrimSpace(stripTags(sub[1]))
	})
	s = reBlockEnd.ReplaceAllString(s, "\n")
	s = stripTags(s)
	s = html.UnescapeString(s)

	lines := strings.Split(s, "\n")
	var cleaned []string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		cleaned = append(cleaned, l)
	}
	res := strings.Join(cleaned, "\n")
	res = reMultiBlank.ReplaceAllString(res, "\n\n")
	return strings.TrimSpace(res)
}

func stripTags(s string) string {
	return reAllTags.ReplaceAllString(s, "")
}

func truncateRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "\n...[truncated]"
}

var (
	reDDGResult  = regexp.MustCompile(`(?is)<a[^>]+class="[^"]*result__a[^"]*"[^>]+href="([^"]+)"[^>]*>(.*?)</a>`)
	reDDGSnippet = regexp.MustCompile(`(?is)<a[^>]+class="[^"]*result__snippet[^"]*"[^>]*>(.*?)</a>`)
)

func searchDuckDuckGo(ctx context.Context, client *http.Client, query string, maxResults int) ([]SearchResult, error) {
	form := url.Values{}
	form.Set("q", query)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://html.duckduckgo.com/html/", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("duckduckgo search: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return nil, err
	}
	page := string(bodyBytes)
	links := reDDGResult.FindAllStringSubmatch(page, maxResults)
	snippets := reDDGSnippet.FindAllStringSubmatch(page, maxResults)

	var out []SearchResult
	for i, m := range links {
		if len(m) < 3 {
			continue
		}
		rawURL := html.UnescapeString(strings.TrimSpace(m[1]))
		if u, err := url.Parse(rawURL); err == nil {
			if uddg := u.Query().Get("uddg"); uddg != "" {
				rawURL = uddg
			}
		}
		title := html.UnescapeString(strings.TrimSpace(stripTags(m[2])))
		var snip string
		if i < len(snippets) && len(snippets[i]) >= 2 {
			snip = html.UnescapeString(strings.TrimSpace(stripTags(snippets[i][1])))
		}
		out = append(out, SearchResult{
			Title:   title,
			URL:     rawURL,
			Snippet: snip,
		})
	}
	return out, nil
}

func searchBrave(ctx context.Context, client *http.Client, apiKey, query string, maxResults int) ([]SearchResult, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("brave search requires web.search_api_key")
	}
	u := fmt.Sprintf("https://api.search.brave.com/res/v1/web/search?q=%s&count=%d", url.QueryEscape(query), maxResults)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var parsed struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	var out []SearchResult
	for _, r := range parsed.Web.Results {
		out = append(out, SearchResult{Title: r.Title, URL: r.URL, Snippet: r.Description})
	}
	return out, nil
}

func searchTavily(ctx context.Context, client *http.Client, apiKey, query string, maxResults int) ([]SearchResult, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("tavily search requires web.search_api_key")
	}
	payload, _ := json.Marshal(map[string]any{
		"api_key":     apiKey,
		"query":       query,
		"max_results": maxResults,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.tavily.com/search", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var parsed struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	var out []SearchResult
	for _, r := range parsed.Results {
		out = append(out, SearchResult{Title: r.Title, URL: r.URL, Snippet: r.Content})
	}
	return out, nil
}

func searchSearXNG(ctx context.Context, client *http.Client, baseURL, query string, maxResults int) ([]SearchResult, error) {
	if baseURL == "" {
		return nil, fmt.Errorf("searxng search requires web.searxng_url")
	}
	u := strings.TrimRight(baseURL, "/") + "/search?format=json&q=" + url.QueryEscape(query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var parsed struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	var out []SearchResult
	for i, r := range parsed.Results {
		if i >= maxResults {
			break
		}
		out = append(out, SearchResult{Title: r.Title, URL: r.URL, Snippet: r.Content})
	}
	return out, nil
}
