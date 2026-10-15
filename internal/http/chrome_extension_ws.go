package http

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

// WHY: per-tab HTTP long-polling produced a request every few seconds for each
// open tab. The extension service worker now keeps a single WebSocket for all
// tabs; commands are pushed as soon as they are enqueued.
const (
	// The extension pings every 20s to keep its MV3 service worker alive, so a
	// silent connection past this window is considered dead.
	chromeExtensionWSIdleTimeout = 75 * time.Second
	chromeExtensionWSReadLimit   = 1 << 20
)

type chromeExtensionWSConn struct {
	wake chan struct{}
}

func newChromeExtensionWSConn() *chromeExtensionWSConn {
	return &chromeExtensionWSConn{wake: make(chan struct{}, 1)}
}

func (c *chromeExtensionWSConn) wakeUp() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// chromeExtensionWSMessage is the client->server frame: register, unregister or ping.
// Command results still go through POST /commands/{id}/result.
type chromeExtensionWSMessage struct {
	Type string `json:"type"`
	chromeExtensionRegisterRequest
}

// chromeExtensionWSCommand is the server->client frame delivering one command.
type chromeExtensionWSCommand struct {
	Type    string                      `json:"type"`
	PageID  string                      `json:"page_id"`
	Command *chromeExtensionCommandWire `json:"command"`
}

func (b *chromeExtensionBridge) attachWS(conn *chromeExtensionWSConn, req chromeExtensionRegisterRequest) bool {
	pageID := strings.TrimSpace(req.PageID)
	if pageID == "" {
		return false
	}
	now := time.Now().UTC()
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cleanupLocked(now)
	page := b.upsertPageLocked(pageID, req.ClientID, req.ExtensionVersion, req.Page, now)
	page.WS = conn
	// Deliver commands that were queued before the page (re)attached.
	if len(page.Queue) > 0 {
		conn.wakeUp()
	}
	return true
}

func (b *chromeExtensionBridge) touchWS(conn *chromeExtensionWSConn) {
	now := time.Now().UTC()
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, page := range b.pages {
		if page.WS == conn {
			page.LastSeenAt = now
		}
	}
}

// detachWS removes pages owned by conn (all of them when pageID is empty) and
// fails their pending commands right away instead of letting tools wait for the timeout.
func (b *chromeExtensionBridge) detachWS(conn *chromeExtensionWSConn, pageID string) {
	b.mu.Lock()
	removed := map[string]bool{}
	for id, page := range b.pages {
		if page.WS != conn || (pageID != "" && id != pageID) {
			continue
		}
		removed[id] = true
		delete(b.pages, id)
	}
	var failed []*chromeExtensionCommandState
	for id, command := range b.commands {
		if removed[command.PageID] {
			failed = append(failed, command)
			delete(b.commands, id)
		}
	}
	b.mu.Unlock()

	for _, command := range failed {
		select {
		case command.ResultCh <- chromeExtensionCommandResult{
			PageID:     command.PageID,
			Error:      "Chrome extension page disconnected: " + command.PageID,
			Result:     []byte("null"),
			ReceivedAt: time.Now().UTC(),
		}:
		default:
		}
	}
}

func (b *chromeExtensionBridge) takeQueuedWS(conn *chromeExtensionWSConn) []chromeExtensionWSCommand {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []chromeExtensionWSCommand
	for _, page := range b.pages {
		if page.WS != conn {
			continue
		}
		for _, commandID := range page.Queue {
			if command, ok := b.commands[commandID]; ok {
				out = append(out, chromeExtensionWSCommand{Type: "command", PageID: page.PageID, Command: command.toWire()})
			}
		}
		page.Queue = nil
	}
	return out
}

// chromeExtensionWSOriginAllowed blocks arbitrary websites: WebSockets bypass CORS,
// so only the extension (or non-browser clients without Origin) may connect.
func chromeExtensionWSOriginAllowed(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return u.Scheme == "chrome-extension" || strings.EqualFold(u.Host, r.Host)
}

func (s *Server) handleChromeExtensionWS(w http.ResponseWriter, r *http.Request) {
	if !chromeExtensionWSOriginAllowed(r) {
		s.errorResponse(w, http.StatusForbidden, "origin not allowed")
		return
	}
	// Origin is validated above; nhooyr's own check only compares hosts.
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(chromeExtensionWSReadLimit)

	bridge := s.chromeExtensionBridge
	conn := newChromeExtensionWSConn()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	defer bridge.detachWS(conn, "")

	go func() {
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			case <-conn.wake:
				for _, msg := range bridge.takeQueuedWS(conn) {
					if err := wsjson.Write(ctx, c, msg); err != nil {
						return
					}
				}
			}
		}
	}()

	for {
		readCtx, readCancel := context.WithTimeout(ctx, chromeExtensionWSIdleTimeout)
		var msg chromeExtensionWSMessage
		err := wsjson.Read(readCtx, c, &msg)
		readCancel()
		if err != nil {
			return
		}
		switch msg.Type {
		case "register":
			bridge.attachWS(conn, msg.chromeExtensionRegisterRequest)
		case "unregister":
			if pageID := strings.TrimSpace(msg.PageID); pageID != "" {
				bridge.detachWS(conn, pageID)
			}
		default:
			bridge.touchWS(conn)
		}
	}
}
