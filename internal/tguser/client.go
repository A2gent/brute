// Package tguser reads Telegram chats on behalf of a user account (MTProto).
//
// The Bot API cannot see a user's private chats, so this package logs in with
// the user's own account and exposes strictly read-only operations. Callers are
// responsible for restricting which chats may be read (allowlist).
package tguser

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
)

// Credentials identify a logged-in Telegram user session.
type Credentials struct {
	APIID   int
	APIHash string
	Session []byte
}

// Dialog is a chat visible to the logged-in account.
type Dialog struct {
	ID    string `json:"id"` // Bot-API style chat id
	Kind  string `json:"kind"`
	Title string `json:"title"`
}

// Message is a single text message from a chat.
type Message struct {
	ID   int       `json:"id"`
	Date time.Time `json:"date"`
	From string    `json:"from"`
	Text string    `json:"text"`
}

// ReadResult is the outcome of ReadMessages.
type ReadResult struct {
	Messages  []Message `json:"messages"`
	Truncated bool      `json:"truncated"` // older unread messages were skipped due to limit
}

// Run connects with the stored session, runs fn and disconnects.
// It returns the (possibly refreshed) session bytes.
func Run(ctx context.Context, creds Credentials, fn func(ctx context.Context, api *tg.Client) error) ([]byte, error) {
	if creds.APIID == 0 || strings.TrimSpace(creds.APIHash) == "" {
		return nil, fmt.Errorf("api_id and api_hash are required")
	}
	if len(creds.Session) == 0 {
		return nil, fmt.Errorf("telegram user session is empty; log in first")
	}
	mem := &session.StorageMemory{}
	if err := mem.StoreSession(ctx, creds.Session); err != nil {
		return nil, err
	}
	client := telegram.NewClient(creds.APIID, creds.APIHash, telegram.Options{SessionStorage: mem})
	err := client.Run(ctx, func(ctx context.Context) error {
		status, err := client.Auth().Status(ctx)
		if err != nil {
			return err
		}
		if !status.Authorized {
			return fmt.Errorf("telegram session is no longer authorized; log in again")
		}
		return fn(ctx, client.API())
	})
	data, _ := mem.Bytes(nil)
	if len(data) == 0 {
		data = creds.Session
	}
	return data, err
}

type peerInfo struct {
	dialog  Dialog
	input   tg.InputPeerClass
	photoID int64 // 0 when the chat has no avatar
}

// channelIDOffset converts channel ids to Bot-API style ids.
const channelIDOffset = int64(1000000000000)

func collectPeers(res tg.MessagesDialogsClass) (map[string]peerInfo, []string, error) {
	mod, ok := res.AsModified()
	if !ok {
		return nil, nil, fmt.Errorf("unexpected dialogs response %T", res)
	}
	users := map[int64]*tg.User{}
	for _, u := range mod.GetUsers() {
		if user, ok := u.(*tg.User); ok {
			users[user.ID] = user
		}
	}
	chats := map[int64]tg.ChatClass{}
	for _, c := range mod.GetChats() {
		chats[c.GetID()] = c
	}
	out := map[string]peerInfo{}
	var order []string
	add := func(info peerInfo) {
		if _, dup := out[info.dialog.ID]; dup {
			return
		}
		out[info.dialog.ID] = info
		order = append(order, info.dialog.ID)
	}
	for _, d := range mod.GetDialogs() {
		dlg, ok := d.(*tg.Dialog)
		if !ok {
			continue
		}
		switch p := dlg.Peer.(type) {
		case *tg.PeerUser:
			u := users[p.UserID]
			if u == nil {
				continue
			}
			add(peerInfo{
				dialog:  Dialog{ID: strconv.FormatInt(u.ID, 10), Kind: "user", Title: userName(u)},
				input:   &tg.InputPeerUser{UserID: u.ID, AccessHash: u.AccessHash},
				photoID: userPhotoID(u),
			})
		case *tg.PeerChat:
			if c, ok := chats[p.ChatID].(*tg.Chat); ok {
				add(peerInfo{
					dialog:  Dialog{ID: strconv.FormatInt(-c.ID, 10), Kind: "group", Title: c.Title},
					input:   &tg.InputPeerChat{ChatID: c.ID},
					photoID: chatPhotoID(c.Photo),
				})
			}
		case *tg.PeerChannel:
			if c, ok := chats[p.ChannelID].(*tg.Channel); ok {
				kind := "supergroup"
				if c.Broadcast {
					kind = "channel"
				}
				add(peerInfo{
					dialog:  Dialog{ID: strconv.FormatInt(-channelIDOffset-c.ID, 10), Kind: kind, Title: c.Title},
					input:   &tg.InputPeerChannel{ChannelID: c.ID, AccessHash: c.AccessHash},
					photoID: chatPhotoID(c.Photo),
				})
			}
		}
	}
	return out, order, nil
}

func userName(u *tg.User) string {
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if name == "" {
		name = u.Username
	}
	if name == "" {
		name = strconv.FormatInt(u.ID, 10)
	}
	return name
}

func fetchPeers(ctx context.Context, api *tg.Client) (map[string]peerInfo, []string, error) {
	res, err := api.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{
		OffsetPeer: &tg.InputPeerEmpty{},
		Limit:      200,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("get dialogs: %w", err)
	}
	return collectPeers(res)
}

// ListDialogs returns recent dialogs of the account.
func ListDialogs(ctx context.Context, creds Credentials) ([]Dialog, []byte, error) {
	var dialogs []Dialog
	sess, err := Run(ctx, creds, func(ctx context.Context, api *tg.Client) error {
		peers, order, err := fetchPeers(ctx, api)
		if err != nil {
			return err
		}
		for _, id := range order {
			dialogs = append(dialogs, peers[id].dialog)
		}
		return nil
	})
	return dialogs, sess, err
}

// ReadMessages returns text messages with id > minID in chronological order.
// At most limit messages are returned (the newest ones if more exist).
func ReadMessages(ctx context.Context, creds Credentials, chatID string, minID, limit int) (*ReadResult, []byte, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	result := &ReadResult{}
	sess, err := Run(ctx, creds, func(ctx context.Context, api *tg.Client) error {
		peers, _, err := fetchPeers(ctx, api)
		if err != nil {
			return err
		}
		peer, ok := peers[chatID]
		if !ok {
			return fmt.Errorf("chat %s not found among recent dialogs", chatID)
		}
		offsetID := 0
		for len(result.Messages) < limit {
			res, err := api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
				Peer:     peer.input,
				OffsetID: offsetID,
				MinID:    minID,
				Limit:    100,
			})
			if err != nil {
				return fmt.Errorf("get history: %w", err)
			}
			mod, ok := res.AsModified()
			if !ok {
				return fmt.Errorf("unexpected history response %T", res)
			}
			names := map[int64]string{}
			for _, u := range mod.GetUsers() {
				if user, ok := u.(*tg.User); ok {
					names[user.ID] = userName(user)
				}
			}
			msgs := mod.GetMessages()
			if len(msgs) == 0 {
				break
			}
			oldest := 0
			for _, m := range msgs {
				msg, ok := m.(*tg.Message)
				if !ok {
					continue
				}
				if oldest == 0 || msg.ID < oldest {
					oldest = msg.ID
				}
				text := strings.TrimSpace(msg.Message)
				if text == "" {
					if msg.Media != nil {
						text = "[media]"
					} else {
						continue
					}
				}
				result.Messages = append(result.Messages, Message{
					ID:   msg.ID,
					Date: time.Unix(int64(msg.Date), 0).UTC(),
					From: senderName(msg, names, peer.dialog),
					Text: text,
				})
			}
			if len(msgs) < 100 || oldest <= minID+1 || oldest == 0 {
				break
			}
			offsetID = oldest
		}
		return nil
	})
	if err != nil {
		return nil, sess, err
	}
	sort.Slice(result.Messages, func(i, j int) bool { return result.Messages[i].ID < result.Messages[j].ID })
	if len(result.Messages) > limit {
		result.Messages = result.Messages[len(result.Messages)-limit:]
		result.Truncated = true
	}
	return result, sess, nil
}

func senderName(msg *tg.Message, names map[int64]string, d Dialog) string {
	if msg.Out {
		return "me"
	}
	if from, ok := msg.GetFromID(); ok {
		if p, ok := from.(*tg.PeerUser); ok {
			if n := names[p.UserID]; n != "" {
				return n
			}
		}
	}
	if d.Kind == "user" {
		return d.Title
	}
	return "unknown"
}

func userPhotoID(u *tg.User) int64 {
	if p, ok := u.Photo.(*tg.UserProfilePhoto); ok {
		return p.PhotoID
	}
	return 0
}

func chatPhotoID(photo tg.ChatPhotoClass) int64 {
	if p, ok := photo.(*tg.ChatPhoto); ok {
		return p.PhotoID
	}
	return 0
}
