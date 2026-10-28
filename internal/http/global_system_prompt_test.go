package http

import (
	"encoding/json"
	nethttp "net/http"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/agent"
	"github.com/A2gent/brute/internal/agentdef"
	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func newGlobalPromptTestServer(t *testing.T) (*Server, *session.Session) {
	t.Helper()
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	manager := session.NewManager(store)
	server := NewServer(config.DefaultConfig(), nil, tools.NewManager("."), manager, store, speechcache.New(0), 0)
	sess, err := manager.Create("build")
	if err != nil {
		t.Fatal(err)
	}
	return server, sess
}

func TestGlobalSystemPromptFollowsAgentPrompt(t *testing.T) {
	server, sess := newGlobalPromptTestServer(t)
	settings := map[string]string{
		agentBaseSystemPromptSettingKey: "You are the orchestrator.",
		globalSystemPromptSettingKey:    "Always answer in English.",
	}
	snapshot := server.composeSystemPromptSnapshotWithSettings(sess, settings)
	combined := snapshot.CombinedPrompt
	role, global := strings.Index(combined, "You are the orchestrator."), strings.Index(combined, "Always answer in English.")
	if role != 0 || global < 0 {
		t.Fatalf("global prompt must follow the agent's own prompt:\n%s", combined)
	}
	if !snapshotHasBlock(snapshot, globalSystemPromptBlockType, "Always answer in English.") {
		t.Fatalf("global prompt not shown as its own block: %+v", snapshot.Blocks)
	}
}

func TestChildRuntimeReadsGlobalSystemPromptFromEnv(t *testing.T) {
	server, sess := newGlobalPromptTestServer(t)
	// A child's own settings are empty; the parent passes its role prompt and
	// the global prompt through the environment.
	t.Setenv("AAGENT_SYSTEM_PROMPT", "You are the ops agent.")
	t.Setenv(globalSystemPromptSettingKey, "Always answer in English.")
	combined := server.composeSystemPromptSnapshotWithSettings(sess, map[string]string{}).CombinedPrompt
	if !strings.HasPrefix(combined, "You are the ops agent.") || !strings.Contains(combined, "Always answer in English.") {
		t.Fatalf("child prompt lacks its role or the global prompt:\n%s", combined)
	}
}

func TestEmptyGlobalSystemPromptAddsNothing(t *testing.T) {
	server, sess := newGlobalPromptTestServer(t)
	snapshot := server.composeSystemPromptSnapshotWithSettings(sess, map[string]string{globalSystemPromptSettingKey: "   "})
	if snapshotHasBlock(snapshot, globalSystemPromptBlockType, "") {
		t.Fatalf("blank global prompt rendered: %+v", snapshot.Blocks)
	}
}

func TestChildGlobalPromptEnvAndStaleness(t *testing.T) {
	english := map[string]string{globalSystemPromptSettingKey: "Always answer in English."}
	labels, env := map[string]string{}, map[string]string{}
	applyChildGlobalSystemPrompt(labels, env, english)
	if env[globalSystemPromptSettingKey] != "Always answer in English." {
		t.Fatalf("child env: %#v", env)
	}
	if globalSystemPromptStale(labels, english) {
		t.Fatal("fresh child reported stale")
	}
	// Editing the prompt, or a child spawned before this setting, restarts it.
	if !globalSystemPromptStale(labels, map[string]string{globalSystemPromptSettingKey: "Answer in Estonian."}) || !globalSystemPromptStale(map[string]string{}, english) {
		t.Fatal("changed or missing global prompt not detected")
	}
	// Editing an agent identity prompt in Caesar must restart warm children too.
	withIdentity := map[string]string{globalSystemPromptSettingKey: "Always answer in English.", subAgentIdentityPromptSettingKey: "You are {{name}}."}
	if !globalSystemPromptStale(labels, withIdentity) {
		t.Fatal("changed agent identity prompt not detected")
	}
	if globalSystemPromptStale(map[string]string{}, map[string]string{}) {
		t.Fatal("children without a global prompt must not restart")
	}
}

func TestSubAgentPromptPrefixUsesConfiguredTemplates(t *testing.T) {
	defaults := subAgentPromptPrefix(map[string]string{}, "Reviewer", true)
	if !strings.HasPrefix(defaults, `You are a sub-agent named "Reviewer".`) || !strings.Contains(defaults, agent.DefaultSystemPrompt()) {
		t.Fatalf("default prefix: %q", defaults)
	}
	settings := map[string]string{
		subAgentIdentityPromptSettingKey:   "Ты {{name}}. Отвечай по-русски.",
		subAgentBaseSystemPromptSettingKey: "Custom base.",
	}
	if got := subAgentPromptPrefix(settings, "Reviewer", true); got != "Ты Reviewer. Отвечай по-русски.\n\nCustom base." {
		t.Fatalf("configured prefix: %q", got)
	}
	if got := subAgentPromptPrefix(settings, "Reviewer", false); got != "Ты Reviewer. Отвечай по-русски." {
		t.Fatalf("prefix without built-in tools: %q", got)
	}
	// Children read the parent's value from the environment.
	t.Setenv(subAgentIdentityPromptSettingKey, "Inherited {{name}}")
	if got := subAgentPromptPrefix(map[string]string{}, "Reviewer", false); got != "Inherited Reviewer" {
		t.Fatalf("inherited prefix: %q", got)
	}
}

func snapshotHasBlock(snapshot *systemPromptSnapshot, blockType, content string) bool {
	if snapshot == nil {
		return false
	}
	for _, block := range snapshot.Blocks {
		if block.Type == blockType && strings.Contains(block.ResolvedContent, content) {
			return true
		}
	}
	return false
}

func TestHostChildReceivesGlobalPromptAndRestartsWhenItChanges(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(hostAgentHelperEnv, "1")
	server, store := newUnifiedAgentsTestServer(t)
	def := &agentdef.Definition{
		Agent:   agentdef.AgentMeta{ID: "global-prompt-child", Name: "Child"},
		Runtime: agentdef.Runtime{Type: agentdef.RuntimeHost},
	}
	workspace, err := server.resolveDockerWorkspaceBinding(def, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.hostRuntime.stopAll)
	childGlobalPrompt := func() string {
		agent, err := server.dockerRuntime.ensureAgentContainerForWorkspace(t.Context(), def, workspace)
		if err != nil {
			t.Fatalf("ensure host agent: %v", err)
		}
		resp, err := nethttp.Get(agent.APIURL + "/env")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var got map[string]string
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		return got["globalPrompt"]
	}

	if err := store.SaveSettings(map[string]string{globalSystemPromptSettingKey: "Always answer in English."}); err != nil {
		t.Fatal(err)
	}
	if got := childGlobalPrompt(); got != "Always answer in English." {
		t.Fatalf("child global prompt = %q", got)
	}
	if err := store.SaveSettings(map[string]string{globalSystemPromptSettingKey: "Answer in Estonian."}); err != nil {
		t.Fatal(err)
	}
	if got := childGlobalPrompt(); got != "Answer in Estonian." {
		t.Fatalf("warm child kept the old global prompt: %q", got)
	}
}
