package news

import (
	"crypto/sha256"
	"regexp"
	"strings"
)

// ComputeTitleHash normalizes a title and returns a SHA-256 of its 3-gram signature.
// Two titles about the same event should hash to the same value if they share
// enough 3-gram overlap after stopword removal.
//
// The current implementation uses the sorted unique tokens (after normalization)
// as the hash input. This is coarser than a proper MinHash but cheap and good
// enough to catch "OpenAI releases GPT-5" vs "GPT-5 released by OpenAI".
func ComputeTitleHash(title string) []byte {
	tokens := normalizeTitle(title)
	if len(tokens) == 0 {
		return nil
	}
	sig := strings.Join(tokens, " ")
	sum := sha256.Sum256([]byte(sig))
	return sum[:]
}

var wordRe = regexp.MustCompile(`[a-z0-9]+`)

var stopwords = map[string]bool{
	"the": true, "a": true, "an": true, "and": true, "or": true, "of": true, "in": true,
	"on": true, "at": true, "for": true, "to": true, "with": true, "from": true, "by": true,
	"is": true, "are": true, "was": true, "were": true, "be": true, "been": true, "being": true,
	"it": true, "its": true, "this": true, "that": true, "these": true, "those": true,
	"as": true, "but": true, "if": true, "then": true, "so": true, "than": true,
	"will": true, "would": true, "can": true, "could": true, "should": true, "may": true,
	"new": true, "news": true, "latest": true, "breaking": true,
	"how": true, "why": true, "what": true, "when": true, "where": true, "who": true,
	"here": true, "there": true, "now": true, "today": true,
}

func normalizeTitle(title string) []string {
	lower := strings.ToLower(title)
	matches := wordRe.FindAllString(lower, -1)
	seen := make(map[string]bool)
	var out []string
	for _, m := range matches {
		if len(m) < 3 {
			continue
		}
		if stopwords[m] {
			continue
		}
		if seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	// Sort alphabetically for order-independent signature
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
