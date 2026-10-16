package http

// Syncs LinkedIn profiles (scraped by adapter-chrome in sync mode) into person cards.
// The link is the card's socials.linkedin URL; scraped details go into a "## LinkedIn"
// body section that is fully rewritten on every sync, so manual notes elsewhere survive.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

const linkedInSectionHeading = "## LinkedIn"

var linkedInProfilePath = regexp.MustCompile(`^/in/([^/?#]+)`)

type linkedInProfile struct {
	URL        string   `json:"url"`
	Name       string   `json:"name"`
	Headline   string   `json:"headline"`
	Company    string   `json:"company"`
	Role       string   `json:"role"`
	Location   string   `json:"location"`
	About      string   `json:"about"`
	Experience []string `json:"experience"`
	Education  []string `json:"education"`
}

type personLinkedInSyncRequest struct {
	Path    string          `json:"path"`
	Profile linkedInProfile `json:"profile"`
}

// canonicalLinkedInURL reduces any linkedin.com/in/<slug> URL to one stable form used as the link key.
func canonicalLinkedInURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("invalid LinkedIn profile URL")
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "linkedin.com" && !strings.HasSuffix(host, ".linkedin.com") {
		return "", fmt.Errorf("profile URL must be on linkedin.com")
	}
	match := linkedInProfilePath.FindStringSubmatch(parsed.EscapedPath())
	if match == nil {
		return "", fmt.Errorf("profile URL must look like https://www.linkedin.com/in/<id>/")
	}
	return "https://www.linkedin.com/in/" + strings.ToLower(match[1]) + "/", nil
}

func frontmatterSocials(frontmatter map[interface{}]interface{}) map[interface{}]interface{} {
	socials, _ := frontmatter["socials"].(map[interface{}]interface{})
	if socials == nil {
		socials = map[interface{}]interface{}{}
	}
	return socials
}

func sameLinkedInURL(value interface{}, canonical string) bool {
	text, _ := value.(string)
	normalized, err := canonicalLinkedInURL(text)
	return err == nil && normalized == canonical
}

func frontmatterString(frontmatter map[interface{}]interface{}, key string) string {
	text, _ := frontmatter[key].(string)
	return strings.TrimSpace(text)
}

func renderLinkedInSection(profile linkedInProfile, canonical string, syncedAt time.Time) string {
	lines := []string{
		linkedInSectionHeading,
		fmt.Sprintf("<!-- Synced from %s on %s by adapter-chrome; this section is overwritten on every sync. -->", canonical, syncedAt.Format("2006-01-02")),
	}
	if headline := strings.TrimSpace(profile.Headline); headline != "" {
		lines = append(lines, "**Headline**: "+headline)
	}
	if location := strings.TrimSpace(profile.Location); location != "" {
		lines = append(lines, "**Location**: "+location)
	}
	if about := strings.TrimSpace(profile.About); about != "" {
		// Escape heading-like lines so they cannot end the section early on the next sync.
		aboutLines := strings.Split(about, "\n")
		for i, line := range aboutLines {
			if strings.HasPrefix(line, "#") {
				aboutLines[i] = `\` + line
			}
		}
		lines = append(lines, "", "### About", strings.Join(aboutLines, "\n"))
	}
	for _, list := range []struct {
		title string
		items []string
	}{{"Experience", profile.Experience}, {"Education", profile.Education}} {
		items := cleanStringList(list.items)
		if len(items) == 0 {
			continue
		}
		lines = append(lines, "", "### "+list.title)
		for _, item := range items {
			lines = append(lines, "- "+strings.Join(strings.Fields(item), " "))
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

// replaceLinkedInSection swaps the "## LinkedIn" section (up to the next level-1/2 heading) or appends it.
func replaceLinkedInSection(body, section string) string {
	lines := strings.Split(body, "\n")
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == linkedInSectionHeading {
			start = i
			break
		}
	}
	if start < 0 {
		return strings.TrimRight(body, "\n") + "\n\n" + section
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "# ") || strings.HasPrefix(lines[i], "## ") {
			end = i
			break
		}
	}
	before := strings.TrimRight(strings.Join(lines[:start], "\n"), "\n")
	after := strings.TrimLeft(strings.Join(lines[end:], "\n"), "\n")
	result := section
	if before != "" {
		result = before + "\n\n" + section
	}
	if after != "" {
		result += "\n" + after
	}
	return result
}

// syncLinkedInIntoCard links the profile to one card: sets socials.linkedin, fills empty
// role/company/location (manual edits win) and rewrites the LinkedIn body section.
func syncLinkedInIntoCard(content string, profile linkedInProfile, canonical string, syncedAt time.Time) (string, error) {
	frontmatter, body, err := splitPersonFrontmatter(content)
	if err != nil {
		return "", err
	}
	if frontmatter == nil {
		frontmatter = map[interface{}]interface{}{}
	}
	socials := frontmatterSocials(frontmatter)
	socials["linkedin"] = canonical
	frontmatter["socials"] = socials
	for key, value := range map[string]string{"role": profile.Role, "company": profile.Company, "location": profile.Location} {
		if value = strings.TrimSpace(value); value != "" && frontmatterString(frontmatter, key) == "" {
			frontmatter[key] = value
		}
	}
	return renderFrontmatterMarkdown(frontmatter, replaceLinkedInSection(body, renderLinkedInSection(profile, canonical, syncedAt)))
}

// unlinkLinkedInFromCard drops socials.linkedin when it points to canonical.
func unlinkLinkedInFromCard(file, canonical string) error {
	content, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	frontmatter, body, err := splitPersonFrontmatter(string(content))
	if err != nil || frontmatter == nil {
		return err
	}
	socials := frontmatterSocials(frontmatter)
	if !sameLinkedInURL(socials["linkedin"], canonical) {
		return nil
	}
	delete(socials, "linkedin")
	if len(socials) == 0 {
		delete(frontmatter, "socials")
	}
	updated, err := renderFrontmatterMarkdown(frontmatter, body)
	if err != nil {
		return err
	}
	return os.WriteFile(file, []byte(updated), 0o644)
}

// handleSyncProjectPersonLinkedIn links a LinkedIn profile to a person card and writes its details.
// A profile belongs to at most one person, so syncing removes the link from every other card.
func (s *Server) handleSyncProjectPersonLinkedIn(w http.ResponseWriter, r *http.Request) {
	project, root, err := s.projectPeopleRoot(chi.URLParam(r, "projectID"))
	if err != nil {
		s.peopleErrorResponse(w, err)
		return
	}
	var req personLinkedInSyncRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	canonical, err := canonicalLinkedInURL(req.Profile.URL)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	directory := findPeopleDirectory(project, root)
	if strings.TrimSpace(req.Path) == "" || !pathWithinPeopleDirectory(req.Path, directory) {
		s.errorResponse(w, http.StatusBadRequest, "Person path must be inside the people directory")
		return
	}
	targetFile, normalized, err := resolveProjectPath(root, req.Path)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	content, err := os.ReadFile(targetFile)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Person card not found")
		return
	}
	updated, err := syncLinkedInIntoCard(string(content), req.Profile, canonical, time.Now())
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to update person card: "+err.Error())
		return
	}

	peopleRoot := filepath.Join(root, filepath.FromSlash(directory))
	walkErr := filepath.WalkDir(peopleRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
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
		if info, err := entry.Info(); err != nil || info.Size() > maxPersonFileBytes {
			return nil
		}
		// Cards that fail to parse are skipped rather than aborting the whole operation.
		_ = unlinkLinkedInFromCard(path, canonical)
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, os.ErrNotExist) {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to update people: "+walkErr.Error())
		return
	}
	if err := os.WriteFile(targetFile, []byte(updated), 0o644); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to save person card: "+err.Error())
		return
	}
	person, _ := parseProjectPerson(filepath.ToSlash(normalized), directory, updated)
	s.jsonResponse(w, http.StatusOK, person)
}
