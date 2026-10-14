package tguser

import (
	"context"
	"encoding/base64"
	"sync"

	"github.com/gotd/td/tg"
)

const (
	avatarDownloadWorkers = 6
	avatarChunkLimit      = 1 << 20 // small avatars fit in a single chunk
)

// avatarCache maps Telegram photo id to a ready-to-use data URL.
var avatarCache sync.Map

// Avatars returns small profile pictures (as data URLs) for the given chat ids.
// Chats without an avatar are omitted. Results are cached per photo id.
func Avatars(ctx context.Context, creds Credentials, chatIDs []string) (map[string]string, []byte, error) {
	out := map[string]string{}
	var mu sync.Mutex
	sess, err := Run(ctx, creds, func(ctx context.Context, api *tg.Client) error {
		peers, _, err := fetchPeers(ctx, api)
		if err != nil {
			return err
		}
		jobs := make(chan peerInfo)
		var wg sync.WaitGroup
		for i := 0; i < avatarDownloadWorkers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for p := range jobs {
					if url := downloadAvatar(ctx, api, p); url != "" {
						mu.Lock()
						out[p.dialog.ID] = url
						mu.Unlock()
					}
				}
			}()
		}
		for _, id := range chatIDs {
			if p, ok := peers[id]; ok && p.photoID != 0 {
				jobs <- p
			}
		}
		close(jobs)
		wg.Wait()
		return nil
	})
	return out, sess, err
}

func downloadAvatar(ctx context.Context, api *tg.Client, p peerInfo) string {
	if cached, ok := avatarCache.Load(p.photoID); ok {
		return cached.(string)
	}
	res, err := api.UploadGetFile(ctx, &tg.UploadGetFileRequest{
		Location: &tg.InputPeerPhotoFileLocation{Peer: p.input, PhotoID: p.photoID},
		Limit:    avatarChunkLimit,
	})
	if err != nil {
		return ""
	}
	file, ok := res.(*tg.UploadFile)
	if !ok || len(file.Bytes) == 0 {
		return ""
	}
	url := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(file.Bytes)
	avatarCache.Store(p.photoID, url)
	return url
}
