package http

const (
	workflowDefinitionMetadataKey = "workflow_definition"
	workflowStateMetadataKey      = "workflow_state"
	workflowTranscriptMetadataKey = "workflow_transcript"
	workflowContextSeededKey      = "workflow_context_seeded"
)

type workflowDefinitionRuntime struct {
	ID          string
	Name        string
	Description string
	EntryNodeID string
	Nodes       []workflowNodeRuntime
	Edges       []workflowEdgeRuntime
	Policy      workflowPolicyRuntime
}

type workflowNodeRuntime struct {
	ID                  string
	Label               string
	Kind                string
	Ref                 string
	SubAgentID          string
	LocalAgentID        string
	ExternalAgentID     string
	Instruction         string
	WorkerSubAgentID    string
	WorkerLabel         string
	WorkerInstruction   string
	ReviewerSubAgentID  string
	ReviewerLabel       string
	ReviewerInstruction string
	LoopMaxTurns        int
}

type workflowEdgeRuntime struct {
	From string
	To   string
	Mode string
}

type workflowPolicyRuntime struct {
	StopCondition string
	JudgeNodeID   string
	MaxTurns      int
	TimeboxMins   int
}

type workflowRuntimeNodeState struct {
	Status         string `json:"status"`
	ChildSessionID string `json:"childSessionId,omitempty"`
	StartedAt      string `json:"startedAt,omitempty"`
	CompletedAt    string `json:"completedAt,omitempty"`
	Error          string `json:"error,omitempty"`
	OutputPreview  string `json:"outputPreview,omitempty"`
}

type workflowRuntimeState struct {
	WorkflowID   string                               `json:"workflowId,omitempty"`
	WorkflowName string                               `json:"workflowName,omitempty"`
	Status       string                               `json:"status"`
	UpdatedAt    string                               `json:"updatedAt"`
	Nodes        map[string]*workflowRuntimeNodeState `json:"nodes"`
}

type workflowTranscriptEntry struct {
	ID             string `json:"id"`
	NodeID         string `json:"nodeId,omitempty"`
	NodeLabel      string `json:"nodeLabel,omitempty"`
	NodeKind       string `json:"nodeKind,omitempty"`
	ChildSessionID string `json:"childSessionId,omitempty"`
	Role           string `json:"role"`
	Content        string `json:"content"`
	CreatedAt      string `json:"createdAt"`
	Status         string `json:"status,omitempty"`
	Turn           int    `json:"turn,omitempty"`
}

type workflowNodeResult struct {
	nodeID                  string
	nodeLabel               string
	childSessionID          string
	output                  string
	emptyHandoff            bool
	newModificationActivity bool
	workStatus              string
	err                     error
}

type workflowTurnNodeState struct {
	RunCount          int
	LastConsumedByDep map[string]int
}

type workflowGraph struct {
	NodeByID  map[string]workflowNodeRuntime
	Preds     map[string][]string
	Succ      map[string][]string
	SCCByNode map[string]int
	SCCSize   map[int]int
	HasCycle  bool
}
