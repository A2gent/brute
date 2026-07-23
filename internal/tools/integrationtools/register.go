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
	manager.Register(NewJiraQueryTool(store))
	manager.Register(NewCircleCIQueryTool(store))
	manager.Register(NewCodeReviewTool(store))
	manager.Register(NewAppSignalQueryTool(store))
	manager.Register(NewBraveSearchQueryTool(store))
	manager.Register(NewEdgeTTSTool(manager.WorkDir(), clipStore))
	manager.Register(NewElevenLabsTTSTool(store, clipStore))
	manager.Register(NewMacOSSayTTSTool(manager.WorkDir(), clipStore))
	manager.Register(NewPiperTTSTool(manager.WorkDir(), clipStore))
	manager.Register(NewWhisperSTTTool(manager.WorkDir()))
	manager.Register(NewNotifyWebAppTool())
	manager.Register(NewTelegramSendMessageTool(store))
	manager.Register(NewDiscordSendMessageTool(store))
	manager.Register(NewExaSearchQueryTool(store))
	manager.Register(NewTavilySearchTool(store))
	manager.Register(NewPerplexitySearchTool(store))
	manager.Register(NewFetchURLTool())
	manager.Register(NewYoutubeTranscriptTool())
	manager.Register(newBrowserChromeTool(manager.WorkDir(), store))
	manager.Register(NewLeonardoGenerateImageTool(store, sessionManager))
	manager.Register(NewComfyUIGenerateImageTool(store, filepath.Join(manager.WorkDir(), "generated", "comfyui")))
	manager.Register(NewComfyUIRunWorkflowTool(store, manager.WorkDir(), filepath.Join(manager.WorkDir(), "generated", "comfyui")))
}
