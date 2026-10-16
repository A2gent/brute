package http

import "github.com/A2gent/brute/internal/agent"

func (s *Server) stallDetector() *agent.StallDetector {
	if s == nil || s.config == nil || !s.config.Tools.JevStall.Enabled {
		return nil
	}
	client, key := s.systemOneClient()
	if client == nil {
		return nil
	}
	secrets, ok := s.jevEgressSecrets(key)
	if !ok {
		return nil
	}
	policy := s.config.Tools.JevStall
	return &agent.StallDetector{Client: client, Secrets: secrets, Interval: policy.Interval, Window: policy.Window}
}
