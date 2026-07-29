package subagent

import "testing"

func TestGetAgentConfigStepBudgets(t *testing.T) {
	t.Parallel()

	s := &Spawner{model: "test-model"}

	general := s.getAgentConfig(AgentTypeGeneral)
	if general.MaxSteps != defaultSubagentMaxSteps {
		t.Fatalf("general MaxSteps = %d, want %d", general.MaxSteps, defaultSubagentMaxSteps)
	}

	explore := s.getAgentConfig(AgentTypeExplore)
	if explore.MaxSteps != exploreSubagentMaxSteps {
		t.Fatalf("explore MaxSteps = %d, want %d", explore.MaxSteps, exploreSubagentMaxSteps)
	}

	developer := s.getAgentConfig(AgentTypeDeveloper)
	if developer.MaxSteps != defaultSubagentMaxSteps {
		t.Fatalf("developer MaxSteps = %d, want %d", developer.MaxSteps, defaultSubagentMaxSteps)
	}
}
