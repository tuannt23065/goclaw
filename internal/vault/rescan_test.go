package vault

import "testing"

func TestInferScopeFromPath(t *testing.T) {
	tests := []struct {
		path    string
		scope   string
		hasTeam bool
		teamID  string
	}{
		{"notes/doc.md", "personal", false, ""},
		{"web-fetch/page.txt", "personal", false, ""},
		{"report.md", "personal", false, ""},
		{"teams/abc-def-123/report.md", "team", true, "abc-def-123"},
		{"teams/abc-def-123/deep/nested.md", "team", true, "abc-def-123"},
		{"teams/", "personal", false, ""},           // malformed, no team ID
		{"teams", "personal", false, ""},             // no slash
		{"teamsfoo/bar.md", "personal", false, ""},   // not teams/ prefix
		{"telegram/123/teams/x/y.md", "personal", false, ""}, // teams not at root
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			scope, teamID := inferScopeFromPath(tt.path)
			if scope != tt.scope {
				t.Errorf("scope = %q, want %q", scope, tt.scope)
			}
			if tt.hasTeam && (teamID == nil || *teamID != tt.teamID) {
				t.Errorf("teamID = %v, want %q", teamID, tt.teamID)
			}
			if !tt.hasTeam && teamID != nil {
				t.Errorf("teamID = %v, want nil", teamID)
			}
		})
	}
}

func TestInferVaultDocType(t *testing.T) {
	tests := []struct {
		path    string
		docType string
	}{
		{"screenshot.png", "media"},
		{"photo.jpg", "media"},
		{"video.mp4", "media"},
		{"audio.mp3", "media"},
		{"notes/meeting.md", "note"},
		{"report.txt", "note"},
		{"web-fetch/page.html", "note"},
		{"skills/my-skill/SKILL.md", "skill"},
		{"deep/soul.md", "context"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := InferDocType(tt.path)
			if got != tt.docType {
				t.Errorf("InferDocType(%q) = %q, want %q", tt.path, got, tt.docType)
			}
		})
	}
}

func TestInferTitle(t *testing.T) {
	tests := []struct {
		path  string
		title string
	}{
		{"report.md", "report"},
		{"notes/meeting-notes.txt", "meeting-notes"},
		{"deep/nested/file.png", "file"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := InferTitle(tt.path)
			if got != tt.title {
				t.Errorf("InferTitle(%q) = %q, want %q", tt.path, got, tt.title)
			}
		})
	}
}
