package storage

import "database/sql"

type LLMLatencySample struct {
	Provider   string
	TTFTMS     *float64
	DurationMS float64
}

type LLMLatencyStore interface {
	ListLLMLatencySamples(sessionID string) ([]LLMLatencySample, error)
}

func (s *SQLiteStore) ListLLMLatencySamples(sessionID string) ([]LLMLatencySample, error) {
	rows, err := s.sessionReader().Query(`
		SELECT COALESCE(json_extract(metadata, '$.llm_provider'), ''),
		       json_extract(metadata, '$.llm_ttft_ms'),
		       json_extract(metadata, '$.llm_duration_ms')
		FROM messages
		WHERE session_id = ? AND role = 'assistant' AND json_valid(metadata)
		  AND json_extract(metadata, '$.llm_duration_ms') IS NOT NULL`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []LLMLatencySample
	for rows.Next() {
		var sample LLMLatencySample
		var ttft sql.NullFloat64
		if err := rows.Scan(&sample.Provider, &ttft, &sample.DurationMS); err != nil {
			return nil, err
		}
		if ttft.Valid {
			sample.TTFTMS = &ttft.Float64
		}
		out = append(out, sample)
	}
	return out, rows.Err()
}
