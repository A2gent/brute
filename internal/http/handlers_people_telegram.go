package http

// Links Telegram chats to person cards. The link lives in the card's frontmatter
// (telegram_chats) so it stays with the person and is readable by agents and loops.

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v2"
)

const personTelegramChatsKey = "telegram_chats"

type telegramChatLink struct {
	ChatID string `json:"chat_id" yaml:"chat_id"`
	Title  string `json:"title,omitempty" yaml:"title,omitempty"`
}

type personTelegramLinkRequest struct {
	Path   string `json:"path"`
	ChatID string `json:"chat_id"`
	Title  string `json:"title"`
	Linked bool   `json:"linked"`
}

type personTelegramLinkResponse struct {
	ChatID string `json:"chat_id"`
	// Path is the person card the chat is now linked to; empty after unlinking.
	Path string `json:"path"`
}

func cleanTelegramChatLinks(links []telegramChatLink) []telegramChatLink {
	out := make([]telegramChatLink, 0, len(links))
	seen := map[string]struct{}{}
	for _, link := range links {
		id := strings.TrimSpace(link.ChatID)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, telegramChatLink{ChatID: id, Title: strings.TrimSpace(link.Title)})
	}
	return out
}

func frontmatterTelegramChats(frontmatter map[interface{}]interface{}) []telegramChatLink {
	raw, ok := frontmatter[personTelegramChatsKey]
	if !ok {
		return nil
	}
	encoded, err := yaml.Marshal(raw)
	if err != nil {
		return nil
	}
	var links []telegramChatLink
	if yaml.Unmarshal(encoded, &links) != nil {
		return nil
	}
	return cleanTelegramChatLinks(links)
}

func renderFrontmatterMarkdown(frontmatter map[interface{}]interface{}, body string) (string, error) {
	raw, err := yaml.Marshal(frontmatter)
	if err != nil {
		return "", err
	}
	body = strings.TrimLeft(body, "\n")
	return "---\n" + strings.TrimSpace(string(raw)) + "\n---\n\n" + strings.TrimRight(body, "\n") + "\n", nil
}

// updateCardTelegramChats rewrites one card's telegram_chats, leaving everything else untouched.
// It reports whether the file changed.
func updateCardTelegramChats(file string, mutate func([]telegramChatLink) []telegramChatLink) (bool, error) {
	content, err := os.ReadFile(file)
	if err != nil {
		return false, err
	}
	frontmatter, body, err := splitPersonFrontmatter(string(content))
	if err != nil {
		return false, err
	}
	if frontmatter == nil {
		frontmatter = map[interface{}]interface{}{}
	}
	before := frontmatterTelegramChats(frontmatter)
	after := cleanTelegramChatLinks(mutate(append([]telegramChatLink(nil), before...)))
	if len(before) == len(after) {
		same := true
		for i := range before {
			if before[i] != after[i] {
				same = false
				break
			}
		}
		if same {
			return false, nil
		}
	}
	if len(after) == 0 {
		delete(frontmatter, personTelegramChatsKey)
	} else {
		items := make([]map[string]string, 0, len(after))
		for _, link := range after {
			item := map[string]string{"chat_id": link.ChatID}
			if link.Title != "" {
				item["title"] = link.Title
			}
			items = append(items, item)
		}
		frontmatter[personTelegramChatsKey] = items
	}
	updated, err := renderFrontmatterMarkdown(frontmatter, body)
	if err != nil {
		return false, err
	}
	return true, os.WriteFile(file, []byte(updated), 0o644)
}

func removeTelegramChat(links []telegramChatLink, chatID string) []telegramChatLink {
	out := links[:0]
	for _, link := range links {
		if link.ChatID != chatID {
			out = append(out, link)
		}
	}
	return out
}

// handleLinkProjectPersonTelegram links (or unlinks) one Telegram chat to a person card.
// A chat belongs to at most one person, so linking removes it from every other card.
func (s *Server) handleLinkProjectPersonTelegram(w http.ResponseWriter, r *http.Request) {
	project, root, err := s.projectPeopleRoot(chi.URLParam(r, "projectID"))
	if err != nil {
		s.peopleErrorResponse(w, err)
		return
	}
	var req personTelegramLinkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	req.ChatID = strings.TrimSpace(req.ChatID)
	if req.ChatID == "" {
		s.errorResponse(w, http.StatusBadRequest, "chat_id is required")
		return
	}
	directory := findPeopleDirectory(project, root)
	var targetFile, targetPath string
	if req.Linked {
		if strings.TrimSpace(req.Path) == "" {
			s.errorResponse(w, http.StatusBadRequest, "Person path is required")
			return
		}
		if !pathWithinPeopleDirectory(req.Path, directory) {
			s.errorResponse(w, http.StatusBadRequest, "Person path must be inside the people directory")
			return
		}
		resolved, normalized, err := resolveProjectPath(root, req.Path)
		if err != nil {
			s.errorResponse(w, http.StatusBadRequest, err.Error())
			return
		}
		if _, err := os.Stat(resolved); err != nil {
			s.errorResponse(w, http.StatusNotFound, "Person card not found")
			return
		}
		targetFile, targetPath = resolved, filepath.ToSlash(normalized)
	}

	peopleRoot := filepath.Join(root, filepath.FromSlash(directory))
	walkErr := filepath.WalkDir(peopleRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) && path == peopleRoot {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() {
			if path != peopleRoot && strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(entry.Name()), ".md") || path == targetFile {
			return nil
		}
		info, err := entry.Info()
		if err != nil || info.Size() > maxPersonFileBytes {
			return nil
		}
		// Cards that fail to parse are skipped rather than aborting the whole operation.
		_, _ = updateCardTelegramChats(path, func(links []telegramChatLink) []telegramChatLink {
			return removeTelegramChat(links, req.ChatID)
		})
		return nil
	})
	if walkErr != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to update people: "+walkErr.Error())
		return
	}

	if req.Linked {
		if _, err := updateCardTelegramChats(targetFile, func(links []telegramChatLink) []telegramChatLink {
			links = removeTelegramChat(links, req.ChatID)
			return append(links, telegramChatLink{ChatID: req.ChatID, Title: req.Title})
		}); err != nil {
			s.errorResponse(w, http.StatusBadRequest, "Failed to update person card: "+err.Error())
			return
		}
	}
	s.jsonResponse(w, http.StatusOK, personTelegramLinkResponse{ChatID: req.ChatID, Path: targetPath})
}
