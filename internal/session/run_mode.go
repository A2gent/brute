package session

const (
	RunModeDecisionKey       = "run_mode_decision"
	RunModeReadOnlyKey       = "run_mode_read_only"
	RunModeWriteRequestedKey = "run_mode_write_requested"
)

// RunModeDecision preserves classifier evidence separately from the effective queue mode.
type RunModeDecision struct {
	Label      string  `json:"label"`
	Confidence float64 `json:"confidence"`
	Source     string  `json:"source"`
}

func (s *Session) RunModeDecision() *RunModeDecision {
	if s == nil {
		return nil
	}
	switch value := s.Metadata[RunModeDecisionKey].(type) {
	case RunModeDecision:
		return &value
	case map[string]interface{}:
		label, _ := value["label"].(string)
		source, _ := value["source"].(string)
		confidence, _ := value["confidence"].(float64)
		return &RunModeDecision{Label: label, Confidence: confidence, Source: source}
	}
	return nil
}

func (s *Session) IsAutoReadOnly() bool {
	if s == nil {
		return false
	}
	readOnly, _ := s.Metadata[RunModeReadOnlyKey].(bool)
	return readOnly
}

func (s *Session) WriteRequested() bool {
	if s == nil {
		return false
	}
	requested, _ := s.Metadata[RunModeWriteRequestedKey].(bool)
	return requested
}
