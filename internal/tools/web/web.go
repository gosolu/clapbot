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
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/gosolu/clapbot/internal/config"
	"github.com/gosolu/react"
)

var deepseekBaseURL = "https://api.deepseek.com"

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
		NewFetchTool(cfg, client),
		NewSearchTool(cfg, client),
	}
}

// NewFetchTool builds the web_fetch tool.
func NewFetchTool(cfg config.WebConfig, client *http.Client) react.Tool {
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

			fetchProvider := strings.ToLower(strings.TrimSpace(cfg.FetchProvider))
			if fetchProvider == "" && strings.EqualFold(strings.TrimSpace(cfg.SearchProvider), "deepseek") {
				fetchProvider = "deepseek"
			}

			var (
				text string
				err  error
			)
			switch fetchProvider {
			case "deepseek":
				apiKey := resolveDeepSeekAPIKey(cfg.FetchAPIKey, cfg.SearchAPIKey)
				text, err = fetchDeepSeek(ctx, client, apiKey, u)
			default:
				text, err = fetchDirect(ctx, client, u)
			}
			if err != nil {
				return "", err
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

func fetchDirect(ctx context.Context, client *http.Client, u string) (string, error) {
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
	if strings.Contains(ct, "html") || bytes.Contains(body[:min(len(body), 512)], []byte("<html")) {
		return HTMLToMarkdown(string(body)), nil
	}
	return string(body), nil
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
			case "deepseek":
				apiKey := resolveDeepSeekAPIKey(cfg.SearchAPIKey, cfg.FetchAPIKey)
				results, err = searchDeepSeek(ctx, client, apiKey, q, maxRes)
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

func resolveDeepSeekAPIKey(primary, secondary string) string {
	if k := strings.TrimSpace(primary); k != "" {
		return k
	}
	if k := strings.TrimSpace(secondary); k != "" {
		return k
	}
	return strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY"))
}

func searchDeepSeek(ctx context.Context, client *http.Client, apiKey, query string, maxResults int) ([]SearchResult, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("deepseek search requires web.search_api_key or DEEPSEEK_API_KEY")
	}
	if runes := []rune(query); len(runes) > 128 {
		query = string(runes[:128])
	}
	if maxResults < 1 {
		maxResults = 6
	}
	if maxResults > 50 {
		maxResults = 50
	}

	payload, err := json.Marshal(map[string]any{
		"query": query,
		"count": maxResults,
	})
	if err != nil {
		return nil, err
	}

	endpoint := strings.TrimRight(deepseekBaseURL, "/") + "/v1/tools/search"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("deepseek search: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("deepseek search HTTP %d: %s", resp.StatusCode, parseDeepSeekError(body))
	}

	var parsed struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Snippet string `json:"snippet"`
			Summary string `json:"summary"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}

	var out []SearchResult
	for _, r := range parsed.Results {
		snip := strings.TrimSpace(r.Summary)
		if snip == "" {
			snip = strings.TrimSpace(r.Snippet)
		}
		out = append(out, SearchResult{
			Title:   r.Title,
			URL:     r.URL,
			Snippet: snip,
		})
	}
	return out, nil
}

func fetchDeepSeek(ctx context.Context, client *http.Client, apiKey, targetURL string) (string, error) {
	if apiKey == "" {
		return "", fmt.Errorf("deepseek fetch requires web.fetch_api_key, web.search_api_key, or DEEPSEEK_API_KEY")
	}

	payload, err := json.Marshal(map[string]any{
		"url": targetURL,
	})
	if err != nil {
		return "", err
	}

	endpoint := strings.TrimRight(deepseekBaseURL, "/") + "/v1/tools/fetch"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("deepseek fetch %s: %w", targetURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("deepseek fetch HTTP %d from %s: %s", resp.StatusCode, targetURL, parseDeepSeekError(body))
	}

	var parsed struct {
		Title   string `json:"title"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", err
	}

	title := strings.TrimSpace(parsed.Title)
	content := strings.TrimSpace(parsed.Content)
	if title != "" && content != "" && !strings.HasPrefix(content, "#") {
		return "# " + title + "\n\n" + content, nil
	}
	if content != "" {
		return content, nil
	}
	return title, nil
}

func parseDeepSeekError(body []byte) string {
	var errResp struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &errResp); err == nil && strings.TrimSpace(errResp.Error.Message) != "" {
		return strings.TrimSpace(errResp.Error.Message)
	}
	return truncateRunes(strings.TrimSpace(string(body)), 500)
}

