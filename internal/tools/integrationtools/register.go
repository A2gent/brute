package integrationtools

import (
	"path/filepath"

	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

// Register wires integration-backed tools into the tool manager.
func Register(manager *tools.Manager, store storage.Store, clipStore *speechcache.Store, sessionManager *session.Manager) {
	if manager == nil || store == nil {
		return
	}
	manager.Register(NewGoogleCalendarQueryTool(store))
	manager.Register(NewGoogleCalendarCreateEventTool(store))
	manager.Register(NewGmailQueryTool(store))
	manager.Register(NewGmailDraftTool(store))
	manager.Register(NewJiraQueryTool(store))
	manager.Register(NewCircleCIQueryTool(store))
	manager.Register(NewCodeReviewTool(store))
	manager.Register(NewBitbucketCreatePullRequestTool(store, manager.WorkDir()))
	manager.Register(NewAppSignalQueryTool(store))
	manager.Register(NewSentryQueryTool(store))
	manager.Register(NewCloudflareQueryTool(store))
	manager.Register(NewBraveSearchQueryTool(store))
	manager.Register(NewSTTTool(manager.WorkDir()))
	manager.Register(NewNotifyWebAppTool())
	manager.Register(NewTelegramSendMessageTool(store))
	manager.Register(NewTelegramUserListChatsTool(store))
	manager.Register(NewTelegramUserReadMessagesTool(store))
	manager.Register(NewDiscordSendMessageTool(store))
	manager.Register(NewExaSearchQueryTool(store))
	manager.Register(NewTavilySearchTool(store))
	manager.Register(NewPerplexitySearchTool(store))
	manager.Register(NewFetchURLTool())
	manager.Register(NewYoutubeTranscriptTool())
	manager.Register(newBrowserChromeTool(manager.WorkDir(), store))
	manager.Register(NewLeonardoGenerateImageTool(store, filepath.Join(manager.WorkDir(), "generated", "leonardo")))
	manager.Register(NewComfyUIGenerateImageTool(store, filepath.Join(manager.WorkDir(), "generated", "comfyui")))
	manager.Register(NewComfyUIRunWorkflowTool(store, manager.WorkDir(), filepath.Join(manager.WorkDir(), "generated", "comfyui")))
	manager.Register(NewBlenderRenderTool(store, filepath.Join(manager.WorkDir(), "generated", "blender")).WithWorkDir(manager.WorkDir()))
	manager.Register(NewBlenderRunScriptTool(store, manager.WorkDir(), filepath.Join(manager.WorkDir(), "generated", "blender")))
	manager.Register(NewMeshyGenerate3DTool(store, manager.WorkDir(), filepath.Join(manager.WorkDir(), "generated", "meshy")))
	manager.Register(NewTripoGenerate3DTool(store, manager.WorkDir(), filepath.Join(manager.WorkDir(), "generated", "tripo")))
}

// SpeechEngineTools returns the engine-specific TTS implementations keyed by engine name. They are
// not registered with the tool manager: the model uses the generic tts/stt tools, and the server
// dispatches to these by name for the engine selected in the app's Speech settings.
func SpeechEngineTools(workDir string, store storage.Store, clipStore *speechcache.Store) map[string]tools.Tool {
	engines := []tools.Tool{
		NewEdgeTTSTool(workDir, clipStore),
		NewElevenLabsTTSTool(store, clipStore),
		NewMacOSSayTTSTool(workDir, clipStore),
		NewPiperTTSTool(workDir, clipStore),
		NewKokoroTTSTool(clipStore),
		NewQwen3TTSTool(clipStore),
		NewSileroTTSTool(clipStore),
	}
	out := make(map[string]tools.Tool, len(engines))
	for _, tool := range engines {
		out[tool.Name()] = tool
	}
	return out
}
