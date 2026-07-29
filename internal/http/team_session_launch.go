package http

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/teamdef"
	"github.com/google/uuid"
)

// attachTeamRunToCreatedSession records the immutable team_run header for a parent session.
// The dispatch loop can later attach member sessions; Caesar already needs the run id for transcript APIs.
func (s *Server) attachTeamRunToCreatedSession(sess *session.Session, teamID string) error {
	if s == nil || sess == nil || teamID == "" {
		return nil
	}
	team, err := s.store.GetTeam(teamID)
	if err != nil {
		return fmt.Errorf("team not found: %w", err)
	}
	def, err := teamdef.ParseYAML([]byte(team.DefinitionYAML))
	if err != nil {
		return fmt.Errorf("stored team %s is invalid: %w", teamID, err)
	}
	policyJSON, err := json.Marshal(def.Policy)
	if err != nil {
		return fmt.Errorf("failed to encode team policy snapshot: %w", err)
	}
	runID := "run_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	now := time.Now().UTC()
	if err := s.store.SaveTeamRun(&storage.TeamRun{
		ID:         runID,
		TeamID:     team.ID,
		SessionID:  sess.ID,
		Status:     storage.TeamRunStatusRunning,
		PolicyJSON: string(policyJSON),
		StartedAt:  now,
	}); err != nil {
		return fmt.Errorf("failed to create team run: %w", err)
	}
	if sess.Metadata == nil {
		sess.Metadata = map[string]interface{}{}
	}
	sess.Metadata["team_id"] = team.ID
	sess.Metadata["team_run_id"] = runID
	sess.Metadata["launch_target"] = "team"
	sess.Metadata["launch_team_id"] = team.ID
	sess.Metadata["launch_team_name"] = team.Name
	if err := s.sessionManager.Save(sess); err != nil {
		return fmt.Errorf("failed to persist team launch metadata: %w", err)
	}
	return nil
}
