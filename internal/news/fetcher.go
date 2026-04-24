package news

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/mmcdole/gofeed"
)

const userAgent = "GoclawNewsMonitor/1.0 (+https://goclaw.io)"

type Fetcher struct {
	client *http.Client
	parser *gofeed.Parser
}

func NewFetcher() *Fetcher {
	return &Fetcher{
		client: &http.Client{
			Timeout: 15 * time.Second,
		},
		parser: gofeed.NewParser(),
	}
}

// Fetch returns new items from the feed. Respects ETag/Last-Modified for 304.
func (f *Fetcher) Fetch(ctx context.Context, sub FeedSubscription) FetchResult {
	switch sub.SourceType {
	case SourceRSS, SourceAtom, "":
		return f.fetchRSS(ctx, sub)
	case SourceHTML:
		return f.fetchHTML(ctx, sub)
	case SourceSearch:
		return f.fetchViaSearch(ctx, sub)
	default:
		return FetchResult{Err: fmt.Errorf("unsupported source_type %q", sub.SourceType)}
	}
}

func (f *Fetcher) fetchRSS(ctx context.Context, sub FeedSubscription) FetchResult {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sub.URL, nil)
	if err != nil {
		return FetchResult{Err: err}
	}
	req.Header.Set("User-Agent", userAgent)
	if sub.LastETag != "" {
		req.Header.Set("If-None-Match", sub.LastETag)
	}
	if sub.LastModified != "" {
		req.Header.Set("If-Modified-Since", sub.LastModified)
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return FetchResult{Err: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		return FetchResult{NotModified: true, ETag: sub.LastETag, Modified: sub.LastModified}
	}
	if resp.StatusCode >= 400 {
		return FetchResult{Err: fmt.Errorf("HTTP %d from %s", resp.StatusCode, sub.URL)}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024))
	if err != nil {
		return FetchResult{Err: err}
	}

	feed, err := f.parser.ParseString(string(body))
	if err != nil {
		return FetchResult{Err: fmt.Errorf("parse feed: %w", err)}
	}

	out := FetchResult{
		ETag:     resp.Header.Get("ETag"),
		Modified: resp.Header.Get("Last-Modified"),
	}
	for _, entry := range feed.Items {
		if entry.Link == "" || entry.Title == "" {
			continue
		}
		item := FetchedItem{
			URL:     strings.TrimSpace(entry.Link),
			Title:   strings.TrimSpace(entry.Title),
			Summary: excerpt(entry.Description, 400),
		}
		if entry.PublishedParsed != nil {
			t := *entry.PublishedParsed
			item.PublishedAt = &t
		} else if entry.UpdatedParsed != nil {
			t := *entry.UpdatedParsed
			item.PublishedAt = &t
		}
		out.Items = append(out.Items, item)
	}
	return out
}

// fetchHTML scrapes article listings from sites that don't provide RSS.
// Each source gets handled by its URL pattern since HTML structure differs.
func (f *Fetcher) fetchHTML(ctx context.Context, sub FeedSubscription) FetchResult {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sub.URL, nil)
	if err != nil {
		return FetchResult{Err: err}
	}
	req.Header.Set("User-Agent", userAgent)
	if sub.LastETag != "" {
		req.Header.Set("If-None-Match", sub.LastETag)
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return FetchResult{Err: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		return FetchResult{NotModified: true, ETag: sub.LastETag}
	}
	if resp.StatusCode >= 400 {
		return FetchResult{Err: fmt.Errorf("HTTP %d from %s", resp.StatusCode, sub.URL)}
	}

	doc, err := goquery.NewDocumentFromReader(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return FetchResult{Err: err}
	}

	items := extractHTMLItems(doc, sub)
	slog.Debug("news.fetch.html", "source", sub.SourceName, "url", sub.URL, "items", len(items))

	return FetchResult{
		Items: items,
		ETag:  resp.Header.Get("ETag"),
	}
}

// extractHTMLItems picks article links from common list-page patterns.
// Returns at most 20 items to avoid pagination blow-up.
func extractHTMLItems(doc *goquery.Document, sub FeedSubscription) []FetchedItem {
	base, _ := url.Parse(sub.URL)
	seen := make(map[string]bool)
	var items []FetchedItem

	// Candidate selectors (priority-ordered). First match wins per anchor.
	selectors := []string{
		"article a[href]",
		"a[href*='/news/']",
		"a[href*='/blog/']",
		"a[href*='/post/']",
		"a[href*='/article/']",
		"h2 a[href], h3 a[href]",
	}

	for _, sel := range selectors {
		doc.Find(sel).Each(func(_ int, s *goquery.Selection) {
			if len(items) >= 20 {
				return
			}
			href, ok := s.Attr("href")
			if !ok {
				return
			}
			abs := absURL(base, href)
			if abs == "" || seen[abs] {
				return
			}
			if !looksLikeArticle(abs, sub.URL) {
				return
			}
			title := strings.TrimSpace(s.Text())
			if title == "" {
				title = strings.TrimSpace(s.AttrOr("title", ""))
			}
			if len(title) < 12 || len(title) > 300 {
				return
			}
			seen[abs] = true
			items = append(items, FetchedItem{
				URL:   abs,
				Title: title,
			})
		})
		if len(items) >= 10 {
			break
		}
	}
	return items
}

func absURL(base *url.URL, href string) string {
	u, err := url.Parse(strings.TrimSpace(href))
	if err != nil {
		return ""
	}
	if u.IsAbs() {
		return u.String()
	}
	if base == nil {
		return ""
	}
	return base.ResolveReference(u).String()
}

// looksLikeArticle filters out nav/login/category links.
func looksLikeArticle(link, basePage string) bool {
	if strings.Contains(link, "#") {
		link = strings.SplitN(link, "#", 2)[0]
	}
	if link == basePage || link == "" {
		return false
	}
	lower := strings.ToLower(link)
	skipFragments := []string{
		"/login", "/signup", "/subscribe", "/contact", "/about", "/careers",
		"/privacy", "/terms", "/legal", "/security", "/press",
		"/tag/", "/category/", "/author/", "/rss",
		"mailto:", "javascript:",
	}
	for _, s := range skipFragments {
		if strings.Contains(lower, s) {
			return false
		}
	}
	// Require path depth >= 2 (e.g. /news/article-slug, not /news)
	u, err := url.Parse(link)
	if err != nil {
		return false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	return len(parts) >= 2 && len(parts[len(parts)-1]) > 8
}

func excerpt(text string, max int) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	// Strip simple HTML tags
	text = stripHTML(text)
	if len(text) <= max {
		return text
	}
	return text[:max] + "..."
}

func stripHTML(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		switch r {
		case '<':
			in = true
		case '>':
			in = false
		default:
			if !in {
				b.WriteRune(r)
			}
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
