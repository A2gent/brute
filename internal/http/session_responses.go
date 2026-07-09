// session_responses.go keeps session/message response shaping separate from request handling.
package http

import (
	"github.com/A2gent/brute/internal/session"
)

func (s *Server) sessionToResponse(sess *session.Session) SessionResponse {
	parentID := ""
	if sess.ParentID != nil {
		parentID = *sess.ParentID
	}
	projectID := ""
	if sess.ProjectID != nil {
		projectID = *sess.ProjectID
	}
	jobID := ""
	if sess.JobID != nil {
		jobID = *sess.JobID
	}
	provider, model := sessionProviderAndModel(sess)
	routedProvider, routedModel := sessionRoutedProviderAndModel(sess)
	routedRule, routedReason := sessionRoutingRuleAndReason(sess)
	snapshot := sessionSystemPromptSnapshot(sess)
	var snapshotPayload *SystemPromptSnapshotPayload
	if snapshot != nil {
		blocks := make([]SystemPromptBlockSnapshotPayload, len(snapshot.Blocks))
		for i, block := range snapshot.Blocks {
			blocks[i] = SystemPromptBlockSnapshotPayload{
				Type:            block.Type,
				Value:           block.Value,
				Enabled:         block.Enabled,
				ResolvedContent: block.ResolvedContent,
				SourcePath:      block.SourcePath,
				Error:           block.Error,
				EstimatedTokens: block.EstimatedTokens,
			}
		}
		snapshotPayload = &SystemPromptSnapshotPayload{
			BasePrompt:        snapshot.BasePrompt,
			CombinedPrompt:    snapshot.CombinedPrompt,
			BaseEstimated:     snapshot.BaseEstimated,
			CombinedEstimated: snapshot.CombinedEstimated,
			Blocks:            blocks,
		}
	}
	inputTokens, outputTokens := sessionInputOutputTokens(sess)
	totalTokens := inputTokens + outputTokens
	cachedInputTokens := int(metadataNumber(sess.Metadata, "total_cached_input_tokens"))
	reasoningTokens := int(metadataNumber(sess.Metadata, "total_reasoning_tokens"))
	currentContextTokens := int(metadataNumber(sess.Metadata, "current_context_tokens"))
	modelContextWindow := int(metadataNumber(sess.Metadata, "context_window"))
	isOutbound, targetAgentID, targetAgentName := sessionA2AOutboundMeta(sess)

	return SessionResponse{
		ID:                   sess.ID,
		AgentID:              sess.AgentID,
		ParentID:             parentID,
		LinkType:             sessionLinkType(sess),
		JobID:                jobID,
		ProjectID:            projectID,
		Provider:             provider,
		Model:                model,
		RoutedProvider:       routedProvider,
		RoutedModel:          routedModel,
		RoutedRule:           routedRule,
		RoutedReason:         routedReason,
		Title:                sess.Title,
		Summary:              sess.Summary,
		Status:               string(sess.Status),
		ActiveRuns:           s.activeSessionRunCount(sess.ID),
		TotalTokens:          totalTokens,
		InputTokens:          inputTokens,
		OutputTokens:         outputTokens,
		CachedInputTokens:    cachedInputTokens,
		ReasoningTokens:      reasoningTokens,
		CurrentContextTokens: currentContextTokens,
		ModelContextWindow:   modelContextWindow,
		RunDurationSeconds:   sessionRunDurationSeconds(sess.CreatedAt, sess.UpdatedAt, string(sess.Status)),
		TaskProgress:         sess.TaskProgress,
		ProviderFailures:     sessionProviderFailures(sess.Metadata),
		CreatedAt:            sess.CreatedAt,
		UpdatedAt:            sess.UpdatedAt,
		Messages:             s.messagesToResponse(sess.Messages),
		SystemPromptSnapshot: snapshotPayload,
		Metadata:             sess.Metadata,
		A2AOutbound:          isOutbound,
		A2ATargetAgentID:     targetAgentID,
		A2ATargetAgentName:   targetAgentName,
	}
}

func (s *Server) messagesToResponse(messages []session.Message) []MessageResponse {
	resp := make([]MessageResponse, len(messages))
	for i, m := range messages {
		resp[i] = s.messageToResponse(m)
	}
	return resp
}

func (s *Server) messageToResponse(m session.Message) MessageResponse {
	msg := MessageResponse{
		ID:        m.ID,
		Role:      m.Role,
		Content:   m.Content,
		Images:    sessionImagesToPayload(m.Images),
		Metadata:  m.Metadata,
		Timestamp: m.Timestamp,
	}

	if len(m.ToolCalls) > 0 {
		msg.ToolCalls = make([]ToolCallResponse, len(m.ToolCalls))
		for j, tc := range m.ToolCalls {
			msg.ToolCalls[j] = ToolCallResponse{
				ID:               tc.ID,
				Name:             tc.Name,
				Input:            tc.Input,
				ThoughtSignature: tc.ThoughtSignature,
			}
		}
	}

	if len(m.ToolResults) > 0 {
		msg.ToolResults = make([]ToolResultResponse, len(m.ToolResults))
		for j, tr := range m.ToolResults {
			msg.ToolResults[j] = ToolResultResponse{
				ToolCallID: tr.ToolCallID,
				Content:    tr.Content,
				IsError:    tr.IsError,
				Metadata:   tr.Metadata,
				Name:       tr.Name,
				DurationMs: tr.DurationMs,
			}
		}
	}

	return msg
}

func sessionImagesToPayload(images []session.ImageAttachment) []MessageImagePayload {
	if len(images) == 0 {
		return nil
	}
	out := make([]MessageImagePayload, 0, len(images))
	for _, img := range images {
		out = append(out, MessageImagePayload{
			Name:       img.Name,
			MediaType:  img.MediaType,
			DataBase64: img.DataBase64,
			URL:        img.URL,
		})
	}
	return out
}
