package integrationtools

import (
	"path/filepath"

	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

// IsIntegrationBackedTool identifies tools whose integration access must execute
// where the integration store lives. Keep this explicit: local browser, file,
// speech, notification and unauthenticated web tools must not gain parent access.
// Names match Register below and the server-registered OpenAI image tool.
func IsIntegrationBackedTool(name string) bool {
	switch name {
	case "google_calendar_query", "google_calendar_create_event",
		"gmail_query", "gmail_create_draft", "jira_query", "jira_create_issue", "circleci_query",
		"code_review", "bitbucket_create_pull_request", "appsignal_query",
		"sentry_query", "cloudflare_query", "brave_search_query",
		"telegram_send_message", "telegram_user_list_chats", "telegram_user_read_messages",
		"discord_send_message", "exa_search", "tavily_search", "perplexity_search",
		"leonardo_generate_image", "comfyui_generate_image", "comfyui_run_workflow",
		"blender_render", "blender_run_script", "meshy_generate_3d", "tripo_generate_3d",
		"openai_generate_image":
		return true
	}
	return false
}

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
	manager.Register(NewJiraCreateIssueTool(store))
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
