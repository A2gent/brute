package http

import (
	"encoding/json"
	"strings"
	"time"
)

// SessionCatalogNotification excludes transcript content and unrelated tool
// metadata. ID is independent of the SSE cursor and message timestamp so replay,
// active-chat delivery and transcript recovery all deduplicate the same result.
type SessionCatalogNotification struct {
	ID            string `json:"id"`
	CreatedAt     string `json:"created_at"`
	Title         string `json:"title,omitempty"`
	Message       string `json:"message"`
	Level         string `json:"level,omitempty"`
	ImagePath     string `json:"image_path,omitempty"`
	ImageURL      string `json:"image_url,omitempty"`
	AudioClipID   string `json:"audio_clip_id,omitempty"`
	AutoPlayAudio *bool  `json:"auto_play_audio,omitempty"`
}

func (s *Server) publishSessionCatalogNotifications(sessionID string, event ChatStreamEvent) {
	if event.Type != "tool_completed" || event.Message == nil || s.sessionManager == nil {
		return
	}
	var notifications []*SessionCatalogNotification
	for _, result := range event.Message.ToolResults {
		if result.IsError || result.ToolCallID == "" || result.Metadata["webapp_notification"] == nil {
			continue
		}
		payload, err := json.Marshal(result.Metadata["webapp_notification"])
		if err != nil {
			continue
		}
		var notification SessionCatalogNotification
		if json.Unmarshal(payload, &notification) != nil || strings.TrimSpace(notification.Message) == "" {
			continue
		}
		notification.ID = sessionID + ":" + result.ToolCallID
		notification.CreatedAt = event.Message.Timestamp.UTC().Format(time.RFC3339Nano)
		notifications = append(notifications, &notification)
	}
	if len(notifications) == 0 {
		return
	}
	// Only load the row, never the transcript; nested sessions are not global
	// notification sources, matching active-chat and completion recovery policy.
	sess, err := s.sessionManager.GetForDisplay(sessionID, false)
	if err != nil || (sess.ParentID != nil && *sess.ParentID != "") {
		return
	}
	projectID := ""
	if sess.ProjectID != nil {
		projectID = *sess.ProjectID
	}
	for _, notification := range notifications {
		s.publishSessionCatalogEvent(SessionCatalogEvent{
			Type: "webapp_notification", SessionID: sessionID, ProjectID: projectID,
			Notification: notification,
		})
	}
}
