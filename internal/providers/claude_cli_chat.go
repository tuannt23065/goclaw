package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Chat runs the CLI synchronously and returns the final response.
func (p *ClaudeCLIProvider) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	systemPrompt, userMsg, images := extractFromMessages(req.Messages)
	sessionKey := extractStringOpt(req.Options, OptSessionKey)
	model := req.Model
	if model == "" {
		model = p.defaultModel
	}
	if err := validateCLIModel(model); err != nil {
		return nil, err
	}

	unlock := p.lockSession(sessionKey)
	defer unlock()

	workDir := p.ensureWorkDir(sessionKey)
	if systemPrompt != "" {
		p.writeClaudeMD(workDir, systemPrompt)
	}

	cliSessionID := deriveSessionUUID(sessionKey)
	disableTools := extractBoolOpt(req.Options, OptDisableTools)
	bc := bridgeContextFromOpts(req.Options)
	mcpPath := p.resolveMCPConfigPath(ctx, sessionKey, bc)
	// Claude CLI >= v2.1.87 requires matching input/output formats.
	// When images are present, buildArgs adds --input-format stream-json,
	// so output format must also be stream-json.
	outputFmt := "json"
	if len(images) > 0 {
		outputFmt = "stream-json"
	}
	args := p.buildArgs(model, workDir, mcpPath, cliSessionID, outputFmt, len(images) > 0, disableTools)

	var stdin *bytes.Reader
	if len(images) > 0 {
		stdin = buildStreamJSONInput(userMsg, images)
	} else {
		args = append(args, "--", userMsg)
	}

	cmd := exec.CommandContext(ctx, p.cliPath, args...)
	cmd.Dir = workDir
	cmd.Env = filterCLIEnv(os.Environ())
	if stdin != nil {
		cmd.Stdin = stdin
	}

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	slog.Debug("claude-cli exec", "cmd", fmt.Sprintf("%s %s", p.cliPath, strings.Join(args, " ")), "workdir", workDir)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("claude-cli: %w (stderr: %s)", err, stderr.String())
	}

	return parseJSONResponse(output)
}

// ChatStream runs the CLI with stream-json output, calling onChunk for each text delta.
func (p *ClaudeCLIProvider) ChatStream(ctx context.Context, req ChatRequest, onChunk func(StreamChunk)) (*ChatResponse, error) {
	systemPrompt, userMsg, images := extractFromMessages(req.Messages)
	sessionKey := extractStringOpt(req.Options, OptSessionKey)
	model := req.Model
	if model == "" {
		model = p.defaultModel
	}
	if err := validateCLIModel(model); err != nil {
		return nil, err
	}

	slog.Debug("claude-cli: acquiring session lock", "session_key", sessionKey)
	unlock := p.lockSession(sessionKey)
	slog.Debug("claude-cli: session lock acquired", "session_key", sessionKey)
	defer func() {
		unlock()
		slog.Debug("claude-cli: session lock released", "session_key", sessionKey)
	}()

	workDir := p.ensureWorkDir(sessionKey)
	if systemPrompt != "" {
		p.writeClaudeMD(workDir, systemPrompt)
	}

	cliSessionID := deriveSessionUUID(sessionKey)
	disableTools := extractBoolOpt(req.Options, OptDisableTools)
	bc := bridgeContextFromOpts(req.Options)
	mcpPath := p.resolveMCPConfigPath(ctx, sessionKey, bc)
	args := p.buildArgs(model, workDir, mcpPath, cliSessionID, "stream-json", len(images) > 0, disableTools)

	var stdin *bytes.Reader
	if len(images) > 0 {
		stdin = buildStreamJSONInput(userMsg, images)
	} else {
		args = append(args, "--", userMsg)
	}

	cmd := exec.CommandContext(ctx, p.cliPath, args...)
	cmd.WaitDelay = 5 * time.Second // force-close pipes if process lingers after kill
	cmd.Dir = workDir
	cmd.Env = filterCLIEnv(os.Environ())
	if stdin != nil {
		cmd.Stdin = stdin
	}

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("claude-cli stdout pipe: %w", err)
	}

	fullCmd := fmt.Sprintf("%s %s", p.cliPath, strings.Join(args, " "))
	slog.Debug("claude-cli stream exec", "cmd", fullCmd, "workdir", workDir)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("claude-cli start: %w", err)
	}

	// Debug log file: only enabled when GOCLAW_DEBUG=1
	// Each run gets its own timestamped file so logs survive session resets.
	var debugFile *os.File
	if os.Getenv("GOCLAW_DEBUG") == "1" {
		debugLogDir := filepath.Join(workDir, "debug-logs")
		_ = os.MkdirAll(debugLogDir, 0755)
		agentName := extractAgentName(sessionKey)
		ts := time.Now().Format("20060102-150405")
		debugLogPath := filepath.Join(debugLogDir, fmt.Sprintf("%s_%s_%s.log", agentName, model, ts))
		debugFile, _ = os.OpenFile(debugLogPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
		if debugFile != nil {
			fmt.Fprintf(debugFile, "=== CMD: %s\n=== WORKDIR: %s\n=== TIME: %s\n=== SESSION: %s\n\n", fullCmd, workDir, time.Now().Format(time.RFC3339), sessionKey)
			defer debugFile.Close()
		}
	}

	// Parse stream-json line-by-line
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, StdioScanBufInit), StdioScanBufMax)

	var finalResp ChatResponse
	var contentBuf strings.Builder

	// Media save directory: workDir/media/ for images extracted from tool results.
	mediaDir := filepath.Join(workDir, "media")

	for scanner.Scan() {
		if ctx.Err() != nil {
			break // context cancelled (abort) → exit immediately
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		// Write raw line to debug log
		if debugFile != nil {
			fmt.Fprintf(debugFile, "%s\n", line)
		}

		var ev cliStreamEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			slog.Debug("claude-cli: skip malformed stream line", "error", err)
			continue
		}

		switch ev.Type {
		case "assistant":
			if ev.Message == nil {
				continue
			}
			text, thinking := extractStreamContent(ev.Message)
			if text != "" {
				contentBuf.WriteString(text)
				onChunk(StreamChunk{Content: text})
			}
			if thinking != "" {
				onChunk(StreamChunk{Thinking: thinking})
			}

		case "user":
			// Extract images from tool result events (e.g. Playwright MCP screenshots).
			if media := extractToolResultMedia(line, mediaDir); len(media) > 0 {
				finalResp.CLIMedia = append(finalResp.CLIMedia, media...)
			}

		case "result":
			if ev.Result != "" {
				finalResp.Content = ev.Result
			} else {
				finalResp.Content = contentBuf.String()
			}
			finalResp.FinishReason = "stop"
			if ev.Subtype == "error" {
				finalResp.FinishReason = "error"
			}
			if ev.Usage != nil {
				finalResp.Usage = &Usage{
					PromptTokens:     ev.Usage.InputTokens,
					CompletionTokens: ev.Usage.OutputTokens,
					TotalTokens:      ev.Usage.InputTokens + ev.Usage.OutputTokens,
				}
			}
		}
	}

	// Context cancelled (abort): best-effort reap (bounded by WaitDelay), then return.
	if ctx.Err() != nil {
		_ = cmd.Wait()
		return nil, ctx.Err()
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("claude-cli: stream read error: %w", err)
	}

	if err := cmd.Wait(); err != nil {
		if debugFile != nil {
			fmt.Fprintf(debugFile, "\n=== STDERR:\n%s\n=== EXIT ERROR: %v\n", stderrBuf.String(), err)
		}
		// If we got partial content, return it with the error
		if finalResp.Content != "" {
			return &finalResp, nil
		}
		return nil, fmt.Errorf("claude-cli: %w (stderr: %s)", err, stderrBuf.String())
	}
	if debugFile != nil && stderrBuf.Len() > 0 {
		fmt.Fprintf(debugFile, "\n=== STDERR:\n%s\n", stderrBuf.String())
	}

	// Fallback if no "result" event was received
	if finalResp.Content == "" {
		finalResp.Content = contentBuf.String()
		finalResp.FinishReason = "stop"
	}

	onChunk(StreamChunk{Done: true})
	return &finalResp, nil
}

// extractToolResultMedia parses a "user" stream event for tool_result content blocks
// containing images. Handles two patterns:
//  1. Base64 image data (MCP or Anthropic format) — decoded and saved to mediaDir
//  2. File path references via <media:image url="file:///path"> tags — referenced directly
//
// Returns extracted media files for forwarding to channels (e.g. Telegram).
func extractToolResultMedia(line []byte, mediaDir string) []CLIMediaFile {
	var ev struct {
		Type    string `json:"type"`
		Message *struct {
			Content []struct {
				Type    string          `json:"type"`    // "tool_result"
				Content json.RawMessage `json:"content"` // string or array of parts
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(line, &ev); err != nil || ev.Message == nil {
		return nil
	}

	var media []CLIMediaFile
	for _, block := range ev.Message.Content {
		if block.Type != "tool_result" || len(block.Content) == 0 {
			continue
		}

		trimmed := bytes.TrimSpace(block.Content)
		if len(trimmed) == 0 {
			continue
		}

		// content can be a string — check for <media:image> tags with file paths
		if trimmed[0] == '"' {
			var text string
			if err := json.Unmarshal(block.Content, &text); err == nil {
				if mf := parseMediaImageTag(text); mf != nil {
					media = append(media, *mf)
				}
			}
			continue
		}

		// content is an array of parts
		if trimmed[0] != '[' {
			continue
		}
		var parts []cliContentPart
		if err := json.Unmarshal(block.Content, &parts); err != nil {
			continue
		}
		for _, part := range parts {
			switch part.Type {
			case "image":
				// MCP format: {type: "image", data: "base64...", mimeType: "image/png"}
				if part.Data != "" {
					mf := saveBase64Media(part.MimeType, part.Data, mediaDir)
					if mf != nil {
						media = append(media, *mf)
					}
					continue
				}
				// Anthropic API format: {type: "image", source: {type: "base64", ...}}
				if part.Source != nil && part.Source.Data != "" {
					mf := saveBase64Media(part.Source.MediaType, part.Source.Data, mediaDir)
					if mf != nil {
						media = append(media, *mf)
					}
				}
			case "text":
				// Check for <media:image url="file:///path"> in text parts
				if part.Text != "" {
					if mf := parseMediaImageTag(part.Text); mf != nil {
						media = append(media, *mf)
					}
				}
			}
		}
	}
	return media
}

// parseMediaImageTag extracts a file path from <media:image url="file:///path"> tags.
// Returns a CLIMediaFile if the referenced file exists on disk.
func parseMediaImageTag(text string) *CLIMediaFile {
	const prefix = `<media:image url="file://`
	idx := strings.Index(text, prefix)
	if idx < 0 {
		return nil
	}
	rest := text[idx+len(prefix):]
	end := strings.IndexByte(rest, '"')
	if end <= 0 {
		return nil
	}
	filePath := rest[:end]
	if _, err := os.Stat(filePath); err != nil {
		return nil
	}
	ext := strings.ToLower(filepath.Ext(filePath))
	mime := "application/octet-stream"
	switch ext {
	case ".png":
		mime = "image/png"
	case ".jpg", ".jpeg":
		mime = "image/jpeg"
	case ".gif":
		mime = "image/gif"
	case ".webp":
		mime = "image/webp"
	}
	slog.Info("claude-cli: media extracted from file tag", "path", filePath, "mime", mime)
	return &CLIMediaFile{Path: filePath, MimeType: mime}
}

// saveBase64Media decodes base64 image data, saves to mediaDir, and returns a CLIMediaFile.
func saveBase64Media(mimeType, b64data, mediaDir string) *CLIMediaFile {
	data, err := base64.StdEncoding.DecodeString(b64data)
	if err != nil {
		slog.Debug("claude-cli: failed to decode base64 media", "error", err)
		return nil
	}
	if err := os.MkdirAll(mediaDir, 0755); err != nil {
		slog.Warn("claude-cli: failed to create media dir", "dir", mediaDir, "error", err)
		return nil
	}
	ext := ".png" // default
	switch mimeType {
	case "image/jpeg":
		ext = ".jpg"
	case "image/gif":
		ext = ".gif"
	case "image/webp":
		ext = ".webp"
	}
	filename := fmt.Sprintf("cli_media_%d%s", time.Now().UnixNano(), ext)
	path := filepath.Join(mediaDir, filename)
	if err := os.WriteFile(path, data, 0644); err != nil {
		slog.Warn("claude-cli: failed to save media file", "path", path, "error", err)
		return nil
	}
	slog.Info("claude-cli: media extracted from tool result", "path", path, "mime", mimeType, "size", len(data))
	return &CLIMediaFile{Path: path, MimeType: mimeType}
}
