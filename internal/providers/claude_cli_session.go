package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/config"
)

// maxCLISessionFileSize is the threshold above which a session file is
// considered too large to resume. The file is deleted and the CLI starts
// a fresh session. 20MB accommodates normal conversation history; files
// grow beyond this when base64 screenshots accumulate.
const maxCLISessionFileSize = 20 * 1024 * 1024 // 20 MB

// maxDebugLogTotalSize is the maximum total size of all debug log files
// across all CLI workspaces. When exceeded, oldest files are deleted first.
const maxDebugLogTotalSize int64 = 1024 * 1024 * 1024 // 1 GB

// validCLIModels lists accepted model aliases for the Claude CLI.
var validCLIModels = map[string]bool{
	"sonnet": true, "opus": true, "haiku": true,
}

// validateCLIModel checks if a model alias is supported by the Claude CLI.
func validateCLIModel(model string) error {
	if !validCLIModels[model] {
		return fmt.Errorf("claude-cli: unsupported model %q (valid: sonnet, opus, haiku)", model)
	}
	return nil
}

// buildArgs constructs CLI arguments.
// mcpConfigPath is the resolved per-session MCP config file (may differ per call).
func (p *ClaudeCLIProvider) buildArgs(model, workDir, mcpConfigPath string, cliSessionID uuid.UUID, outputFormat string, hasImages, disableTools bool) []string {
	args := []string{
		"-p",
		"--output-format", outputFormat,
		"--model", model,
		"--permission-mode", p.permMode,
		"--verbose",
	}

	if mcpConfigPath != "" {
		args = append(args, "--mcp-config", mcpConfigPath)
	}

	// Session persistence: check if CLI session file exists on disk.
	// If exists → --resume (continue conversation). If not → --session-id (create new).
	// Session files live at ~/.claude/projects/<sanitized-workdir>/<uuid>.jsonl
	sid := cliSessionID.String()
	if sessionFileExists(workDir, cliSessionID) {
		args = append(args, "--resume", sid)
	} else {
		args = append(args, "--session-id", sid)
	}

	if hasImages {
		args = append(args, "--input-format", "stream-json")
	}

	if disableTools {
		// Summoner: disable all tools entirely via disallowedTools
		args = append(args, "--disallowedTools", "Bash,Edit,Read,Write,Glob,Grep,WebFetch,WebSearch,TodoRead,TodoWrite,NotebookRead,NotebookEdit")
	} else if mcpConfigPath != "" {
		// Chat with MCP bridge: route file/shell tool execution through GoClaw's
		// controlled MCP bridge so each call is logged + sandboxed + tenant-scoped.
		// WebFetch + WebSearch are deliberately KEPT enabled — GoClaw has no MCP
		// equivalent for them, and disabling them would silently strip web access
		// from every agent (which is what happened: agents would hallucinate news
		// instead of fetching it). They make outbound HTTP only, no FS / shell
		// side effects, so the security delta versus letting CLI handle them
		// directly is negligible.
		args = append(args, "--disallowedTools", "Bash,Edit,Read,Write,Glob,Grep,TodoRead,TodoWrite,NotebookRead,NotebookEdit")
	}

	if p.hooksSettingsPath != "" {
		args = append(args, "--settings", p.hooksSettingsPath)
	}

	return args
}

// resolveMCPConfigPath writes a per-session MCP config with agent context and returns its path.
func (p *ClaudeCLIProvider) resolveMCPConfigPath(ctx context.Context, sessionKey string, bc BridgeContext) string {
	if p.mcpConfigData == nil {
		return ""
	}
	path := p.mcpConfigData.WriteMCPConfig(ctx, sessionKey, bc)
	if path != "" {
		p.mcpConfigDirs.Store(filepath.Dir(path), struct{}{})
	}
	return path
}

// ensureWorkDir creates and returns a stable work directory for the given session key.
func (p *ClaudeCLIProvider) ensureWorkDir(sessionKey string) string {
	// Sanitize session key for filesystem safety (path traversal, null bytes, length)
	safe := sanitizePathSegment(sessionKey)
	dir := filepath.Join(p.baseWorkDir, safe)

	p.mu.Lock()
	defer p.mu.Unlock()

	if err := os.MkdirAll(dir, 0755); err != nil {
		slog.Warn("claude-cli: failed to create workdir", "dir", dir, "error", err)
		return os.TempDir()
	}
	return dir
}

// writeClaudeMD writes the system prompt to CLAUDE.md in the work directory.
// CLI reads this file automatically on every run (including --resume).
// Skips write if content is unchanged to avoid unnecessary disk I/O.
func (p *ClaudeCLIProvider) writeClaudeMD(workDir, systemPrompt string) {
	path := filepath.Join(workDir, "CLAUDE.md")
	if existing, err := os.ReadFile(path); err == nil && string(existing) == systemPrompt {
		return
	}
	if err := os.WriteFile(path, []byte(systemPrompt), 0600); err != nil {
		slog.Warn("claude-cli: failed to write CLAUDE.md", "path", path, "error", err)
	}
}

// extractFromMessages extracts system prompt, last user message, and images from the messages array.
func extractFromMessages(msgs []Message) (systemPrompt, userMsg string, images []ImageContent) {
	for _, m := range msgs {
		if m.Role == "system" {
			systemPrompt = m.Content
		}
	}
	// Find last user message
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			userMsg = msgs[i].Content
			images = msgs[i].Images
			break
		}
	}
	return
}

// extractStringOpt gets a string value from Options map by key.
func extractStringOpt(opts map[string]any, key string) string {
	if opts == nil {
		return ""
	}
	if v, ok := opts[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// extractBoolOpt gets a bool value from Options map by key.
func extractBoolOpt(opts map[string]any, key string) bool {
	if opts == nil {
		return false
	}
	if v, ok := opts[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

// extractAgentName extracts the agent key from a session key string.
// Session key format: "agent:<agent-key>:..." → returns "<agent-key>".
// Falls back to "unknown" if the format doesn't match.
func extractAgentName(sessionKey string) string {
	parts := strings.SplitN(sessionKey, ":", 3)
	if len(parts) >= 2 && parts[0] == "agent" {
		return parts[1]
	}
	return "unknown"
}

// bridgeContextFromOpts builds a BridgeContext from the Options map.
func bridgeContextFromOpts(opts map[string]any) BridgeContext {
	return BridgeContext{
		AgentID:   extractStringOpt(opts, OptAgentID),
		UserID:    extractStringOpt(opts, OptUserID),
		Channel:   extractStringOpt(opts, OptChannel),
		ChatID:    extractStringOpt(opts, OptChatID),
		PeerKind:  extractStringOpt(opts, OptPeerKind),
		Workspace: extractStringOpt(opts, OptWorkspace),
		TenantID:  extractStringOpt(opts, OptTenantID),
	}
}

// defaultCLIWorkDir returns dataDir/cli-workspaces.
func defaultCLIWorkDir() string {
	return filepath.Join(config.ResolvedDataDirFromEnv(), "cli-workspaces")
}

// deriveSessionUUID creates a deterministic UUID v5 from a session key string.
func deriveSessionUUID(sessionKey string) uuid.UUID {
	if sessionKey == "" {
		return uuid.New() // fallback: random
	}
	return uuid.NewSHA1(uuid.NameSpaceDNS, []byte(sessionKey))
}

// sessionFilePath returns the path to the Claude CLI session .jsonl file,
// or "" if the home directory cannot be determined.
func sessionFilePath(workDir string, sessionID uuid.UUID) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	// Resolve symlinks to match CLI's path encoding (macOS: /var → /private/var)
	resolved, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		resolved = workDir
	}
	// Claude CLI stores sessions at: ~/.claude/projects/<encoded-path>/<session-id>.jsonl
	// CLI replaces path separators, "_", ".", and ":" with "-" in the path encoding.
	// On Windows: C:\Users\foo → C--Users-foo (backslash + colon both become "-")
	// On macOS/Linux: /home/foo → -home-foo (forward slash becomes "-")
	encoded := strings.NewReplacer(string(filepath.Separator), "-", "_", "-", ".", "-", ":", "-").Replace(resolved)
	return filepath.Join(home, ".claude", "projects", encoded, sessionID.String()+".jsonl")
}

// sessionFileExists checks if a Claude CLI session file exists and is small enough to resume.
// If the file exceeds maxCLISessionFileSize, it is deleted and false is returned
// so the CLI starts a fresh session instead of failing on an oversized context.
func sessionFileExists(workDir string, sessionID uuid.UUID) bool {
	path := sessionFilePath(workDir, sessionID)
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if info.Size() > maxCLISessionFileSize {
		slog.Warn("claude-cli: session file too large, rotating",
			"path", path,
			"size_mb", info.Size()/(1024*1024),
			"threshold_mb", maxCLISessionFileSize/(1024*1024),
		)
		if err := os.Remove(path); err != nil {
			slog.Error("claude-cli: failed to remove oversized session file",
				"path", path, "error", err)
		}
		return false
	}
	return true
}

// buildStreamJSONInput creates stream-json stdin for multimodal input
// (images/documents + text). The content block type is chosen from MIME:
//   - application/pdf → "document" (Anthropic PDF support, Claude 3.5+)
//   - image/*         → "image"
//
// MIME types that fit neither are emitted as "image" for backwards
// compatibility; the upstream Anthropic API will reject them explicitly
// rather than silently misroute.
func buildStreamJSONInput(text string, images []ImageContent) *bytes.Reader {
	var contentBlocks []map[string]any

	for _, img := range images {
		blockType := "image"
		if img.MimeType == "application/pdf" {
			blockType = "document"
		}
		contentBlocks = append(contentBlocks, map[string]any{
			"type": blockType,
			"source": map[string]any{
				"type":       "base64",
				"media_type": img.MimeType,
				"data":       img.Data,
			},
		})
	}

	if text != "" {
		contentBlocks = append(contentBlocks, map[string]any{
			"type": "text",
			"text": text,
		})
	}

	msg := map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": contentBlocks,
		},
	}

	data, _ := json.Marshal(msg)
	return bytes.NewReader(data)
}

// ResetCLISession deletes the Claude CLI session file and CLAUDE.md for a given session key.
// Called on /reset to ensure the CLI starts fresh instead of --resume-ing poisoned history.
// Safe to call even if CLI provider is not in use (no-op if files don't exist).
func ResetCLISession(baseWorkDir, sessionKey string) {
	if baseWorkDir == "" {
		baseWorkDir = defaultCLIWorkDir()
	}
	safe := sanitizePathSegment(sessionKey)
	workDir := filepath.Join(baseWorkDir, safe)
	sessionID := deriveSessionUUID(sessionKey)

	// Delete CLI session .jsonl file from ~/.claude/projects/
	if sessionFile := sessionFilePath(workDir, sessionID); sessionFile != "" {
		if err := os.Remove(sessionFile); err == nil {
			slog.Info("claude-cli: deleted session file on /reset", "path", sessionFile)
		}
	}

	// Delete CLAUDE.md from workdir so it regenerates fresh
	claudeMD := filepath.Join(workDir, "CLAUDE.md")
	if err := os.Remove(claudeMD); err == nil {
		slog.Info("claude-cli: deleted CLAUDE.md on /reset", "path", claudeMD)
	}
}

// filterCLIEnv removes CLAUDE* env vars to prevent nested session conflicts,
// but preserves CLAUDE_CODE_OAUTH_TOKEN for authentication.
func filterCLIEnv(environ []string) []string {
	var filtered []string
	for _, e := range environ {
		key := e
		if before, _, ok := strings.Cut(e, "="); ok {
			key = before
		}
		// Filter out variables that could cause nested CLI conflicts,
		// but preserve auth token needed by the subprocess.
		if strings.HasPrefix(key, "CLAUDE") && key != "CLAUDE_CODE_OAUTH_TOKEN" {
			continue
		}
		filtered = append(filtered, e)
	}
	return filtered
}

// debugLogFile holds path and mod time for sorting during cleanup.
type debugLogFile struct {
	path    string
	size    int64
	modTime int64 // unix nano
}

// pruneDebugLogs enforces maxDebugLogTotalSize across all CLI workspace debug-logs.
// Called after each debug log write. Deletes oldest files first until under budget.
func pruneDebugLogs(baseWorkDir string) {
	if baseWorkDir == "" {
		baseWorkDir = defaultCLIWorkDir()
	}

	// Collect all .log files under */debug-logs/
	var files []debugLogFile
	var totalSize int64

	entries, err := os.ReadDir(baseWorkDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		debugDir := filepath.Join(baseWorkDir, entry.Name(), "debug-logs")
		logs, err := os.ReadDir(debugDir)
		if err != nil {
			continue
		}
		for _, logEntry := range logs {
			if logEntry.IsDir() {
				continue
			}
			info, err := logEntry.Info()
			if err != nil {
				continue
			}
			sz := info.Size()
			totalSize += sz
			files = append(files, debugLogFile{
				path:    filepath.Join(debugDir, logEntry.Name()),
				size:    sz,
				modTime: info.ModTime().UnixNano(),
			})
		}
	}

	if totalSize <= maxDebugLogTotalSize {
		return
	}

	// Sort oldest first
	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime < files[j].modTime
	})

	deleted := 0
	for _, f := range files {
		if totalSize <= maxDebugLogTotalSize {
			break
		}
		if err := os.Remove(f.path); err == nil {
			totalSize -= f.size
			deleted++
		}
	}
	if deleted > 0 {
		slog.Info("claude-cli: pruned debug logs", "deleted", deleted, "remaining_mb", totalSize/(1024*1024))
	}
}
