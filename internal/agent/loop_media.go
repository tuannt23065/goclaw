package agent

import (
	"os"
	"path/filepath"
	"strings"
)

// parseMediaResult extracts a MediaResult from a tool result string containing "MEDIA:" prefix.
// Handles formats: "MEDIA:/path/to/file" and "[[audio_as_voice]]\nMEDIA:/path/to/file".
// Returns nil if no MEDIA: prefix is found.
//
// IMPORTANT: Only matches "MEDIA:" at the start of the (trimmed) string to avoid false
// positives when tool output contains "MEDIA:" in arbitrary text (e.g. a web page
// mentioning a commit message like "return MEDIA: path from screenshot").
func parseMediaResult(toolOutput string) *MediaResult {
	s := toolOutput
	asVoice := false

	// Check for [[audio_as_voice]] tag (TTS voice messages)
	if strings.Contains(s, "[[audio_as_voice]]") {
		asVoice = true
		s = strings.ReplaceAll(s, "[[audio_as_voice]]", "")
	}

	s = strings.TrimSpace(s)

	// Only match MEDIA: at the beginning of the string.
	if !strings.HasPrefix(s, "MEDIA:") {
		return nil
	}
	path := strings.TrimSpace(s[6:])
	if path == "" {
		return nil
	}
	// Take only the first line (in case there's trailing text)
	if nl := strings.IndexByte(path, '\n'); nl >= 0 {
		path = strings.TrimSpace(path[:nl])
	}

	return &MediaResult{
		Path:        path,
		ContentType: mimeFromExt(filepath.Ext(path)),
		AsVoice:     asVoice,
	}
}

// deduplicateMedia removes duplicate media results by path, keeping the first occurrence.
func deduplicateMedia(media []MediaResult) []MediaResult {
	if len(media) <= 1 {
		return media
	}
	seen := make(map[string]bool, len(media))
	result := make([]MediaResult, 0, len(media))
	for _, m := range media {
		if seen[m.Path] {
			continue
		}
		seen[m.Path] = true
		result = append(result, m)
	}
	return result
}

// parseMediaImageTags scans text for <media:image url="file:///path"> tags and returns
// MediaResults for files that exist on disk. Used to extract media from CLI provider
// responses where tools (e.g. Playwright MCP) embed file references in text output.
func parseMediaImageTags(text string) []MediaResult {
	const prefix = `<media:image url="file://`
	var results []MediaResult
	remaining := text
	for {
		idx := strings.Index(remaining, prefix)
		if idx < 0 {
			break
		}
		rest := remaining[idx+len(prefix):]
		end := strings.IndexByte(rest, '"')
		if end <= 0 {
			break
		}
		filePath := rest[:end]
		remaining = rest[end:]

		if _, err := os.Stat(filePath); err != nil {
			continue
		}
		results = append(results, MediaResult{
			Path:        filePath,
			ContentType: mimeFromExt(filepath.Ext(filePath)),
		})
	}
	return results
}

// mimeFromExt returns a MIME type for common media file extensions.
func mimeFromExt(ext string) string {
	switch strings.ToLower(ext) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".mp4":
		return "video/mp4"
	case ".ogg", ".opus":
		return "audio/ogg"
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	case ".txt":
		return "text/plain"
	case ".pdf":
		return "application/pdf"
	case ".csv":
		return "text/csv"
	case ".json":
		return "application/json"
	case ".html", ".htm":
		return "text/html"
	case ".xml":
		return "application/xml"
	case ".zip":
		return "application/zip"
	case ".doc", ".docx":
		return "application/msword"
	case ".xls", ".xlsx":
		return "application/vnd.ms-excel"
	case ".md":
		return "text/markdown"
	default:
		return "application/octet-stream"
	}
}
