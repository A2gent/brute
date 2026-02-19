package integrationtools

import (
	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

// Register wires integration-backed tools into the tool manager.
func Register(manager *tools.Manager, store storage.Store, clipStore *speechcache.Store) {
	if manager == nil || store == nil {
		return
	}
	manager.Register(NewGoogleCalendarQueryTool(store))
	manager.Register(NewBraveSearchQueryTool(store))
	manager.Register(NewElevenLabsTTSTool(store, clipStore))
	manager.Register(NewMacOSSayTTSTool(manager.WorkDir(), clipStore))
	manager.Register(NewPiperTTSTool(manager.WorkDir(), clipStore))
	manager.Register(NewWhisperSTTTool(manager.WorkDir()))
	manager.Register(NewNotifyWebAppTool())
	manager.Register(NewTelegramSendMessageTool(store))
	manager.Register(NewExaSearchQueryTool(store))
	manager.Register(NewFetchURLTool())
	manager.Register(NewBrowserChromeTool(manager.WorkDir()))
}
