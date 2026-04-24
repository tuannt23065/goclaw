package news

import (
	"strings"
	"time"
)

// ScoreHeuristic returns a 0..10 score combining keyword match, source priority,
// recency, and title length penalty. Tuned for English tech news.
func ScoreHeuristic(item FeedItem, source FeedSubscription) int {
	score := 0
	title := strings.ToLower(item.Title + " " + item.Summary)

	// Source priority (1..10) contributes up to +4
	score += source.Priority * 4 / 10

	// High-signal keywords: +2 each (max +4)
	hiKw := 0
	for _, kw := range highSignalKeywords {
		if strings.Contains(title, kw) {
			hiKw++
			if hiKw >= 2 {
				break
			}
		}
	}
	score += hiKw * 2

	// Medium-signal keywords: +1 each (max +2)
	medKw := 0
	for _, kw := range mediumSignalKeywords {
		if strings.Contains(title, kw) {
			medKw++
			if medKw >= 2 {
				break
			}
		}
	}
	score += medKw

	// Negative keywords: -2 each
	for _, kw := range negativeKeywords {
		if strings.Contains(title, kw) {
			score -= 2
		}
	}

	// Recency bonus
	if item.PublishedAt != nil {
		age := time.Since(*item.PublishedAt)
		switch {
		case age < 1*time.Hour:
			score += 2
		case age < 6*time.Hour:
			score += 1
		case age > 48*time.Hour:
			score -= 2
		}
	}

	// Title length sanity (too short = click bait, too long = listicle noise)
	tl := len(item.Title)
	switch {
	case tl < 30:
		score -= 1
	case tl > 200:
		score -= 1
	}

	// Clamp
	if score < 0 {
		score = 0
	}
	if score > 10 {
		score = 10
	}
	return score
}

var highSignalKeywords = []string{
	// AI model releases + companies
	"gpt-5", "gpt-6", "claude", "gemini", "llama", "mistral", "qwen", "deepseek", "grok",
	"anthropic", "openai", "google deepmind", "meta ai", "nvidia",
	"launches", "announces", "unveils", "releases", "introduces", "debut",
	// Major events
	"acquires", "acquisition", "lawsuit", "banned", "breach", "hack", "zero-day",
	"layoffs", "ipo", "funding round", "raises", "valuation",
	// AI technical
	"foundation model", "reasoning model", "multimodal", "agent",
}

var mediumSignalKeywords = []string{
	"ai", "artificial intelligence", "llm", "large language model",
	"apple", "microsoft", "amazon", "samsung", "tesla", "spacex",
	"chatgpt", "copilot", "assistant",
	"security", "vulnerability", "patch",
	"chip", "gpu", "silicon", "processor",
	"open source", "open-source", "github",
	"privacy", "regulation", "eu", "antitrust",
}

var negativeKeywords = []string{
	"sponsored", "advertorial", "best deals", "cyber monday", "black friday",
	"gift guide", "buying guide", "vs.", "review:", "rumor:",
	"horoscope", "celeb", "gossip",
}
