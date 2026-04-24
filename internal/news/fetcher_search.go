package news

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// fetchViaSearch discovers new articles for CF-gated sites by spawning the
// `claude` CLI with the web_search tool. Google-indexed snippets bypass the
// Cloudflare block.
//
// `sub.URL` should be the site prefix (e.g. "openai.com/index").
func (f *Fetcher) fetchViaSearch(ctx context.Context, sub FeedSubscription) FetchResult {
	// Sanitize site hint (strip scheme + trailing slash)
	site := strings.TrimSpace(sub.URL)
	site = strings.TrimPrefix(site, "https://")
	site = strings.TrimPrefix(site, "http://")
	site = strings.TrimSuffix(site, "/")

	prompt := fmt.Sprintf(`Use the web_search tool to find NEW articles from site:%s published in the past 48 hours.

Output ONLY a JSON array (no markdown, no code fences, no prose). Schema:
[{"url":"https://...","title":"...","summary":"1-sentence brief","published_at":"2026-04-23T14:00:00Z"}]

Rules:
- Each url must start with https://%s
- Include at most 10 results
- Skip entries with no discernible publish date
- If nothing found, output exactly: []`, site, site)

	runCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "claude",
		"-p", prompt,
		"--output-format", "text",
		"--model", "haiku",
		"--permission-mode", "bypassPermissions",
		"--disallowedTools", "Bash,Edit,Write,Read,Glob,Grep,NotebookRead,NotebookEdit,TodoRead,TodoWrite",
	)
	out, err := cmd.Output()
	if err != nil {
		return FetchResult{Err: fmt.Errorf("claude-cli search: %w", err)}
	}

	items, parseErr := parseSearchJSON(string(out))
	if parseErr != nil {
		slog.Warn("news.fetch.search.parse", "source", sub.SourceName, "error", parseErr,
			"raw_preview", truncate(string(out), 300))
		return FetchResult{Err: fmt.Errorf("parse search output: %w", parseErr)}
	}
	slog.Debug("news.fetch.search", "source", sub.SourceName, "items", len(items))

	return FetchResult{Items: items}
}

// parseSearchJSON extracts the JSON array from LLM output. Tolerates leading
// prose / trailing notes by locating the first '[' and last ']'.
func parseSearchJSON(raw string) ([]FetchedItem, error) {
	s := strings.TrimSpace(raw)
	// Strip markdown code fences if present
	fence := regexp.MustCompile("```(?:json)?\\s*|```")
	s = fence.ReplaceAllString(s, "")

	start := strings.Index(s, "[")
	end := strings.LastIndex(s, "]")
	if start == -1 || end == -1 || end <= start {
		return nil, fmt.Errorf("no JSON array found")
	}
	slice := s[start : end+1]

	var raws []struct {
		URL         string `json:"url"`
		Title       string `json:"title"`
		Summary     string `json:"summary"`
		PublishedAt string `json:"published_at"`
	}
	if err := json.Unmarshal([]byte(slice), &raws); err != nil {
		return nil, err
	}

	out := make([]FetchedItem, 0, len(raws))
	for _, r := range raws {
		if r.URL == "" || r.Title == "" {
			continue
		}
		it := FetchedItem{
			URL:     strings.TrimSpace(r.URL),
			Title:   strings.TrimSpace(r.Title),
			Summary: strings.TrimSpace(r.Summary),
		}
		if r.PublishedAt != "" {
			if t, err := time.Parse(time.RFC3339, r.PublishedAt); err == nil {
				it.PublishedAt = &t
			}
		}
		out = append(out, it)
	}
	return out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
