package integrationtools

import (
	"github.com/gratheon/aagent/internal/speechcache"
	"github.com/gratheon/aagent/internal/storage"
	"github.com/gratheon/aagent/internal/tools"
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
	manager.Register(NewNotifyWebAppTool())
	manager.Register(NewTelegramSendMessageTool(store))
}
