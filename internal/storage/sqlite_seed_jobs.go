package storage

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
)

// BuiltInArchivedSessionsReviewJobID is the stable ID of the default daily loop
// that reviews archived ("Completed") sessions for self-improvement.
const BuiltInArchivedSessionsReviewJobID = "builtin-archived-sessions-review"

const archivedSessionsReviewCron = "0 3 * * *"

const archivedSessionsReviewPrompt = `Review archived (completed) sessions to improve the agent and the projects it works on.

1. Call archived_sessions with action=list to get archived sessions pending analysis. If none, reply "No archived sessions to review" and stop.
2. For each session, read it with archived_sessions action=get. Look for inefficiencies: wasted or repeated tool calls, failed commands, wrong assumptions, user corrections, misunderstood instructions, slow paths, unfinished work, and good approaches worth repeating.
3. Act on the findings, grouped by the session's project (name, id and folder are shown in the list):
   a. Project tasks: for concrete unfinished work, bugs or follow-ups found in sessions, use the tasks tool (action=create, project_id=<session project id>). First run tasks action=list with a q filter to avoid duplicates. Only create clearly actionable tasks.
   b. Playbook lessons: if the project has a folder, append concise, reusable lessons learned to <project folder>/playbook.md (create it with a "# Playbook" heading if missing). Read the file first, do not duplicate existing lessons, merge or tighten similar ones, keep each lesson to one line with the WHY, and keep the file short.
   c. Agent improvements: collect suggestions for improving the agent itself (system prompt, tools, defaults, workflows). Create at most 3 tasks for the most valuable ones with the tasks tool in project_id=system-agent tagged agent-improvement, after checking for duplicates.
4. Call archived_sessions action=mark_analyzed with the IDs of every session you fully reviewed. Leave sessions you could not review unmarked so the next run retries them.
5. Finish with a short report: sessions reviewed, tasks created (refs), playbook files updated, and agent improvement suggestions.

Be concise and skip sessions with nothing to learn. Never modify project source code in this loop.`

// seedBuiltInRecurringJobs creates default loops once. A builtin_seeds row is
// written alongside, so a user who deletes or edits the job keeps that choice.
func (s *SQLiteStore) seedBuiltInRecurringJobs() error {
	var seededAt time.Time
	err := s.db.QueryRow(`SELECT seeded_at FROM builtin_seeds WHERE id = ?`, BuiltInArchivedSessionsReviewJobID).Scan(&seededAt)
	if err == nil {
		return nil
	}
	if err != sql.ErrNoRows {
		return fmt.Errorf("failed to inspect built-in seed: %w", err)
	}

	now := time.Now()
	schedule, err := cron.ParseStandard(archivedSessionsReviewCron)
	if err != nil {
		return fmt.Errorf("invalid built-in job schedule: %w", err)
	}
	nextRun := schedule.Next(now)
	job := &RecurringJob{
		ID:               BuiltInArchivedSessionsReviewJobID,
		Name:             "Archived sessions review",
		ScheduleHuman:    "every day at 3am",
		ScheduleCron:     archivedSessionsReviewCron,
		TaskPrompt:       archivedSessionsReviewPrompt,
		TaskPromptSource: "text",
		RunTarget:        "agent",
		Enabled:          true,
		NextRunAt:        &nextRun,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := s.SaveJob(job); err != nil {
		return err
	}
	if _, err := s.db.Exec(`INSERT INTO builtin_seeds (id, seeded_at) VALUES (?, ?)`, BuiltInArchivedSessionsReviewJobID, now); err != nil {
		return fmt.Errorf("failed to record built-in seed: %w", err)
	}
	return nil
}
