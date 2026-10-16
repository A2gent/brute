package session

import (
	"fmt"
	"github.com/A2gent/brute/internal/storage"
	"strings"
)

func (m *Manager) ImageRoot() string {
	if store, ok := m.store.(interface{ ImageRoot() string }); ok {
		return store.ImageRoot()
	}
	return ""
}

func (m *Manager) fromStorage(row *storage.Session) *Session {
	sess := FromStorage(row)
	for i := range sess.Messages {
		for j := range sess.Messages[i].Images {
			sess.Messages[i].Images[j].Root = m.ImageRoot()
		}
	}
	return sess
}

func (m *Manager) saveImages(sess *Session) error {
	root := m.ImageRoot()
	if root == "" {
		return nil
	}
	for i := range sess.Messages {
		msg := &sess.Messages[i]
		for j, img := range msg.Images {
			if img.DataBase64 == "" && img.Path != "" && img.Root == root && strings.HasPrefix(img.Path, "sessions/"+sess.ID+"/") {
				continue
			}
			// Linked continuations must own their copy, so deleting the parent cannot
			// remove an attachment still needed by the child session.
			if img.Path != "" && (img.Root == "" || img.Root != root || !strings.HasPrefix(img.Path, "sessions/"+sess.ID+"/")) {
				if img.Root == "" {
					img.Root = root
				}
				var err error
				img, err = img.Inline()
				if err != nil {
					return fmt.Errorf("load image: %w", err)
				}
			}
			saved, err := img.Save(root, sess.ID)
			if err != nil {
				return fmt.Errorf("save image for message %s: %w", msg.ID, err)
			}
			msg.Images[j] = saved
			if img.DataBase64 != "" || saved.Path != img.Path {
				sess.MarkMessageChanged(msg.ID)
			}
		}
	}
	return nil
}
