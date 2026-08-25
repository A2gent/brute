package http

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
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/A2gent/brute/internal/storage"
	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v2"
)

const (
	defaultPeopleDirectory = "People"
	peopleDirectorySetting = "people_directory"
	maxPersonFileBytes     = 512 * 1024
)

var (
	errPeopleProjectNotFound  = errors.New("people project not found")
	peopleDirectoryCandidates = []string{"People", "people", "08-Люди", "Люди"}
	legacyPersonBlocklist     = regexp.MustCompile(`(?i)(^|[ _-])(meeting|report|analysis|critique|timetable|procedure|project|проект|отчет|отчёт|анализ|обучение|школа|учителя|работа|заказ|книжк|дерево)([ _-]|$)`)
	markdownImagePattern      = regexp.MustCompile(`!\[[^\]]*\]\(([^)]+)\)`)
	obsidianImagePattern      = regexp.MustCompile(`!\[\[([^\]|]+)(?:\|[^\]]+)?\]\]`)
	markdownLinkPattern       = regexp.MustCompile(`(^|[^!])\[[^\]]*\]\(([^)]+)\)`)
	obsidianLinkPattern       = regexp.MustCompile(`(^|[^!])\[\[([^\]|]+)(?:\|[^\]]+)?\]\]`)
	legacyGroupPrefixPattern  = regexp.MustCompile(`^\s*(\d+)\s*[-–—]\s*(.+?)\s*$`)
)

type projectPerson struct {
	Path          string            `json:"path"`
	Name          string            `json:"name"`
	Photo         string            `json:"photo,omitempty"`
	PhotoPath     string            `json:"photo_path"`
	Groups        []string          `json:"groups"`
	Importance    int               `json:"importance"`
	Relationship  string            `json:"relationship"`
	Company       string            `json:"company"`
	Role          string            `json:"role"`
	Location      string            `json:"location"`
	Birthday      string            `json:"birthday"`
	DeathDate     string            `json:"death_date"`
	LastContacted string            `json:"last_contacted"`
	NextFollowUp  string            `json:"next_follow_up"`
	Phones        []string          `json:"phones"`
	Emails        []string          `json:"emails"`
	Socials       map[string]string `json:"socials"`
	Interests     []string          `json:"interests"`
	Traits        []string          `json:"traits"`
	Aliases       []string          `json:"aliases"`
	Tags          []string          `json:"tags"`
	Status        string            `json:"status"`
	Legacy        bool              `json:"legacy"`
	Links         []string          `json:"links"`
}

type projectPeopleResponse struct {
	Directory string          `json:"directory"`
	People    []projectPerson `json:"people"`
}

type projectPersonRequest struct {
	Path          string            `json:"path"`
	Name          string            `json:"name"`
	Photo         string            `json:"photo"`
	Groups        []string          `json:"groups"`
	Importance    int               `json:"importance"`
	Relationship  string            `json:"relationship"`
	Company       string            `json:"company"`
	Role          string            `json:"role"`
	Location      string            `json:"location"`
	Birthday      string            `json:"birthday"`
	DeathDate     string            `json:"death_date"`
	LastContacted string            `json:"last_contacted"`
	NextFollowUp  string            `json:"next_follow_up"`
	Phones        []string          `json:"phones"`
	Emails        []string          `json:"emails"`
	Socials       map[string]string `json:"socials"`
	Interests     []string          `json:"interests"`
	Traits        []string          `json:"traits"`
	Aliases       []string          `json:"aliases"`
	Tags          []string          `json:"tags"`
	Status        string            `json:"status"`
}

type personFrontmatter struct {
	Type          string            `yaml:"type"`
	Name          string            `yaml:"name"`
	Photo         string            `yaml:"photo"`
	Groups        []string          `yaml:"groups"`
	Importance    int               `yaml:"importance"`
	Relationship  string            `yaml:"relationship"`
	Company       string            `yaml:"company"`
	Role          string            `yaml:"role"`
	Location      string            `yaml:"location"`
	Birthday      string            `yaml:"birthday"`
	DeathDate     string            `yaml:"death_date"`
	LastContacted string            `yaml:"last_contacted"`
	NextFollowUp  string            `yaml:"next_follow_up"`
	Phones        []string          `yaml:"phones"`
	Emails        []string          `yaml:"emails"`
	Socials       map[string]string `yaml:"socials"`
	Interests     []string          `yaml:"interests"`
	Traits        []string          `yaml:"traits"`
	Aliases       []string          `yaml:"aliases"`
	Tags          []string          `yaml:"tags"`
	Status        string            `yaml:"status"`
}

func (s *Server) handleListProjectPeople(w http.ResponseWriter, r *http.Request) {
	project, root, err := s.projectPeopleRoot(chi.URLParam(r, "projectID"))
	if err != nil {
		s.peopleErrorResponse(w, err)
		return
	}
	directory := findPeopleDirectory(project, root)
	peopleRoot := filepath.Join(root, filepath.FromSlash(directory))
	if _, err := os.Stat(peopleRoot); errors.Is(err, os.ErrNotExist) {
		s.jsonResponse(w, http.StatusOK, projectPeopleResponse{Directory: directory, People: []projectPerson{}})
		return
	} else if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to access people directory: "+err.Error())
		return
	}

	people := make([]projectPerson, 0)
	personContents := make(map[string]string)
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
		if !strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			return nil
		}
		info, err := entry.Info()
		if err != nil || info.Size() > maxPersonFileBytes {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		person, ok := parseProjectPerson(filepath.ToSlash(relPath), directory, string(content))
		if ok {
			person.Links = []string{}
			people = append(people, person)
			personContents[person.Path] = bodyWithoutFrontmatter(string(content))
		}
		return nil
	})
	if walkErr != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to scan people: "+walkErr.Error())
		return
	}
	resolvePersonLinks(people, personContents)

	sort.SliceStable(people, func(i, j int) bool {
		if people[i].Importance != people[j].Importance {
			return people[i].Importance > people[j].Importance
		}
		return strings.ToLower(people[i].Name) < strings.ToLower(people[j].Name)
	})
	s.jsonResponse(w, http.StatusOK, projectPeopleResponse{Directory: directory, People: people})
}

func (s *Server) handleCreateProjectPerson(w http.ResponseWriter, r *http.Request) {
	project, root, err := s.projectPeopleRoot(chi.URLParam(r, "projectID"))
	if err != nil {
		s.peopleErrorResponse(w, err)
		return
	}
	var req projectPersonRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	if err := validatePersonRequest(req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	directory := findPeopleDirectory(project, root)
	folder := directory
	if len(req.Groups) > 0 {
		folder = filepath.ToSlash(filepath.Join(directory, safePersonPathPart(req.Groups[0])))
	}
	path := filepath.ToSlash(filepath.Join(folder, safePersonFilename(req.Name)+".md"))
	resolved, normalized, err := resolveProjectPath(root, path)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := os.Stat(resolved); err == nil {
		s.errorResponse(w, http.StatusConflict, "A person card with this name already exists")
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		s.errorResponse(w, http.StatusBadRequest, "Failed to access person card: "+err.Error())
		return
	}
	if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to create people group: "+err.Error())
		return
	}
	content, err := renderPersonMarkdown(nil, req, "# "+strings.TrimSpace(req.Name)+"\n")
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := os.WriteFile(resolved, []byte(content), 0o644); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to create person card: "+err.Error())
		return
	}
	person, _ := parseProjectPerson(filepath.ToSlash(normalized), directory, content)
	s.jsonResponse(w, http.StatusCreated, person)
}

func (s *Server) handleSaveProjectPerson(w http.ResponseWriter, r *http.Request) {
	project, root, err := s.projectPeopleRoot(chi.URLParam(r, "projectID"))
	if err != nil {
		s.peopleErrorResponse(w, err)
		return
	}
	var req projectPersonRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	if err := validatePersonRequest(req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.Path) == "" {
		s.errorResponse(w, http.StatusBadRequest, "Person path is required")
		return
	}
	directory := findPeopleDirectory(project, root)
	if !pathWithinPeopleDirectory(req.Path, directory) {
		s.errorResponse(w, http.StatusBadRequest, "Person path must be inside the people directory")
		return
	}
	resolved, normalized, err := resolveProjectPath(root, req.Path)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	content, err := os.ReadFile(resolved)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.errorResponse(w, http.StatusNotFound, "Person card not found")
			return
		}
		s.errorResponse(w, http.StatusBadRequest, "Failed to read person card: "+err.Error())
		return
	}
	frontmatter, body, err := splitPersonFrontmatter(string(content))
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	updated, err := renderPersonMarkdown(frontmatter, req, body)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := os.WriteFile(resolved, []byte(updated), 0o644); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to save person card: "+err.Error())
		return
	}
	person, _ := parseProjectPerson(filepath.ToSlash(normalized), directory, updated)
	s.jsonResponse(w, http.StatusOK, person)
}

func (s *Server) projectPeopleRoot(projectID string) (*storage.Project, string, error) {
	project, err := s.store.GetProject(projectID)
	if err != nil {
		return nil, "", errPeopleProjectNotFound
	}
	if project.Folder == nil || strings.TrimSpace(*project.Folder) == "" {
		return nil, "", fmt.Errorf("project folder is not configured")
	}
	root, err := filepath.Abs(strings.TrimSpace(*project.Folder))
	if err != nil {
		return nil, "", fmt.Errorf("project folder path is invalid")
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return nil, "", fmt.Errorf("project folder does not exist")
	}
	return project, root, nil
}

func (s *Server) peopleErrorResponse(w http.ResponseWriter, err error) {
	if errors.Is(err, errPeopleProjectNotFound) {
		s.errorResponse(w, http.StatusNotFound, "Project not found")
		return
	}
	s.errorResponse(w, http.StatusBadRequest, err.Error())
}

func findPeopleDirectory(project *storage.Project, root string) string {
	if configured := strings.TrimSpace(project.Settings[peopleDirectorySetting]); configured != "" {
		if _, _, err := resolveProjectPath(root, configured); err == nil {
			return filepath.ToSlash(filepath.Clean(configured))
		}
	}
	for _, candidate := range peopleDirectoryCandidates {
		if info, err := os.Stat(filepath.Join(root, filepath.FromSlash(candidate))); err == nil && info.IsDir() {
			return candidate
		}
	}
	return defaultPeopleDirectory
}

func parseProjectPerson(path, peopleDirectory, content string) (projectPerson, bool) {
	frontmatter, body, err := splitPersonFrontmatter(content)
	if err != nil {
		return projectPerson{}, false
	}
	var metadata personFrontmatter
	hasFrontmatter := frontmatter != nil
	if hasFrontmatter {
		raw, err := yaml.Marshal(frontmatter)
		if err != nil || yaml.Unmarshal(raw, &metadata) != nil {
			return projectPerson{}, false
		}
		if metadata.Type != "" && !strings.EqualFold(strings.TrimSpace(metadata.Type), "person") {
			return projectPerson{}, false
		}
	}

	legacy := !strings.EqualFold(strings.TrimSpace(metadata.Type), "person")
	name := strings.TrimSpace(metadata.Name)
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		name = strings.TrimSpace(strings.TrimLeftFunc(name, func(r rune) bool {
			return unicode.IsSymbol(r) || unicode.IsSpace(r)
		}))
	}
	if legacy && !looksLikeLegacyPerson(path, peopleDirectory, name, content) {
		return projectPerson{}, false
	}

	groups, legacyImportance, company := inferLegacyPersonLocation(path, peopleDirectory)
	if len(metadata.Groups) > 0 {
		groups = cleanStringList(metadata.Groups)
	}
	importance := metadata.Importance
	if importance == 0 {
		importance = legacyImportance
	}
	if importance == 0 {
		importance = 5
	}
	if metadata.Company != "" {
		company = strings.TrimSpace(metadata.Company)
	}
	photo := strings.TrimSpace(metadata.Photo)
	if photo == "" {
		photo = firstMarkdownImage(body)
	}

	return projectPerson{
		Path: filepath.ToSlash(path), Name: name, Photo: photo,
		PhotoPath: normalizePersonPhotoPath(path, peopleDirectory, photo),
		Groups:    groups, Importance: clampImportance(importance),
		Relationship: strings.TrimSpace(metadata.Relationship), Company: company,
		Role: strings.TrimSpace(metadata.Role), Location: strings.TrimSpace(metadata.Location),
		Birthday: strings.TrimSpace(metadata.Birthday), DeathDate: strings.TrimSpace(metadata.DeathDate),
		LastContacted: strings.TrimSpace(metadata.LastContacted), NextFollowUp: strings.TrimSpace(metadata.NextFollowUp),
		Phones: cleanStringList(metadata.Phones), Emails: cleanStringList(metadata.Emails),
		Socials: cleanStringMap(metadata.Socials), Interests: cleanStringList(metadata.Interests),
		Traits: cleanStringList(metadata.Traits), Aliases: cleanStringList(metadata.Aliases),
		Tags: cleanStringList(metadata.Tags), Status: defaultPersonStatus(metadata.Status), Legacy: legacy,
	}, true
}

func splitPersonFrontmatter(content string) (map[interface{}]interface{}, string, error) {
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	if !strings.HasPrefix(normalized, "---\n") {
		return nil, normalized, nil
	}
	end := strings.Index(normalized[4:], "\n---")
	if end < 0 {
		return nil, "", fmt.Errorf("person card has unterminated frontmatter")
	}
	frontmatterText := normalized[4 : 4+end]
	bodyStart := 4 + end + len("\n---")
	body := strings.TrimPrefix(normalized[bodyStart:], "\n")
	frontmatter := map[interface{}]interface{}{}
	if err := yaml.Unmarshal([]byte(frontmatterText), &frontmatter); err != nil {
		return nil, "", fmt.Errorf("invalid person frontmatter: %w", err)
	}
	return frontmatter, body, nil
}

func bodyWithoutFrontmatter(content string) string {
	_, body, err := splitPersonFrontmatter(content)
	if err != nil {
		return content
	}
	return body
}

func resolvePersonLinks(people []projectPerson, contents map[string]string) {
	paths := make(map[string]string, len(people))
	stems := make(map[string]string, len(people))
	for _, person := range people {
		normalizedPath := filepath.ToSlash(filepath.Clean(person.Path))
		paths[strings.ToLower(normalizedPath)] = person.Path
		stem := strings.TrimSuffix(filepath.Base(normalizedPath), filepath.Ext(normalizedPath))
		stems[strings.ToLower(stem)] = person.Path
		stems[strings.ToLower(person.Name)] = person.Path
		for _, alias := range person.Aliases {
			stems[strings.ToLower(alias)] = person.Path
		}
	}

	for index := range people {
		seen := make(map[string]struct{})
		for _, target := range extractPersonLinkTargets(people[index].Path, contents[people[index].Path]) {
			resolved := paths[strings.ToLower(target)]
			if resolved == "" {
				stem := strings.TrimSuffix(filepath.Base(target), filepath.Ext(target))
				resolved = stems[strings.ToLower(stem)]
			}
			if resolved == "" || resolved == people[index].Path {
				continue
			}
			if _, exists := seen[resolved]; exists {
				continue
			}
			seen[resolved] = struct{}{}
			people[index].Links = append(people[index].Links, resolved)
		}
		sort.Strings(people[index].Links)
	}
}

func extractPersonLinkTargets(sourcePath, body string) []string {
	targets := make([]string, 0)
	for _, match := range markdownLinkPattern.FindAllStringSubmatch(body, -1) {
		if len(match) != 3 {
			continue
		}
		if target := normalizePersonLinkTarget(sourcePath, match[2], true); target != "" {
			targets = append(targets, target)
		}
	}
	for _, match := range obsidianLinkPattern.FindAllStringSubmatch(body, -1) {
		if len(match) != 3 {
			continue
		}
		if target := normalizePersonLinkTarget(sourcePath, match[2], false); target != "" {
			targets = append(targets, target)
		}
	}
	return targets
}

func normalizePersonLinkTarget(sourcePath, rawTarget string, relative bool) string {
	target := strings.TrimSpace(strings.Trim(rawTarget, "<>"))
	if index := strings.IndexAny(target, "#?"); index >= 0 {
		target = target[:index]
	}
	if target == "" || strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
		return ""
	}
	if decoded, err := url.PathUnescape(target); err == nil {
		target = decoded
	}
	if !strings.EqualFold(filepath.Ext(target), ".md") {
		target += ".md"
	}
	if relative && !strings.HasPrefix(target, "/") {
		target = filepath.Join(filepath.Dir(sourcePath), filepath.FromSlash(target))
	}
	return filepath.ToSlash(filepath.Clean(strings.TrimPrefix(target, "/")))
}

func renderPersonMarkdown(existing map[interface{}]interface{}, req projectPersonRequest, body string) (string, error) {
	frontmatter := existing
	if frontmatter == nil {
		frontmatter = map[interface{}]interface{}{}
	}
	values := map[string]interface{}{
		"type": "person", "name": strings.TrimSpace(req.Name), "photo": strings.TrimSpace(req.Photo),
		"groups": cleanStringList(req.Groups), "importance": clampImportance(req.Importance),
		"relationship": strings.TrimSpace(req.Relationship), "company": strings.TrimSpace(req.Company),
		"role": strings.TrimSpace(req.Role), "location": strings.TrimSpace(req.Location),
		"birthday": strings.TrimSpace(req.Birthday), "death_date": strings.TrimSpace(req.DeathDate),
		"last_contacted": strings.TrimSpace(req.LastContacted), "next_follow_up": strings.TrimSpace(req.NextFollowUp),
		"phones": cleanStringList(req.Phones), "emails": cleanStringList(req.Emails),
		"socials": cleanStringMap(req.Socials), "interests": cleanStringList(req.Interests),
		"traits": cleanStringList(req.Traits), "aliases": cleanStringList(req.Aliases),
		"tags": cleanStringList(req.Tags), "status": defaultPersonStatus(req.Status),
	}
	for key, value := range values {
		if personMetadataEmpty(key, value) {
			delete(frontmatter, key)
			continue
		}
		frontmatter[key] = value
	}
	raw, err := yaml.Marshal(frontmatter)
	if err != nil {
		return "", fmt.Errorf("failed to encode person frontmatter: %w", err)
	}
	body = strings.TrimLeft(body, "\n")
	return "---\n" + strings.TrimSpace(string(raw)) + "\n---\n\n" + strings.TrimRight(body, "\n") + "\n", nil
}

func personMetadataEmpty(key string, value interface{}) bool {
	if key == "type" || key == "name" || key == "importance" || key == "status" {
		return false
	}
	switch typed := value.(type) {
	case string:
		return typed == ""
	case []string:
		return len(typed) == 0
	case map[string]string:
		return len(typed) == 0
	default:
		return false
	}
}

func validatePersonRequest(req projectPersonRequest) error {
	if strings.TrimSpace(req.Name) == "" {
		return fmt.Errorf("person name is required")
	}
	if req.Importance < 0 || req.Importance > 10 {
		return fmt.Errorf("importance must be between 1 and 10")
	}
	return nil
}

func looksLikeLegacyPerson(path, peopleDirectory, name, content string) bool {
	if name == "" || strings.EqualFold(strings.TrimSpace(name), "люди") || legacyPersonBlocklist.MatchString(name) {
		return false
	}
	if !pathWithinPeopleDirectory(path, peopleDirectory) {
		return false
	}
	if strings.Contains(content, "## Данные Geni") || strings.Contains(content, "**Geni ID**:") {
		return true
	}
	parts := strings.Split(strings.TrimPrefix(filepath.ToSlash(path), strings.TrimSuffix(peopleDirectory, "/")+"/"), "/")
	if len(parts) < 2 {
		return false
	}
	category := strings.ToLower(parts[0])
	knownCategory := false
	for _, marker := range []string{"дет", "жен", "род", "друз", "партнер", "партнёр", "влюб", "коллег", "свидан", "универс", "знаком", "сосед", "виртуал", "девклуб"} {
		if strings.Contains(category, marker) {
			knownCategory = true
			break
		}
	}
	return knownCategory
}

func inferLegacyPersonLocation(path, peopleDirectory string) ([]string, int, string) {
	relative := strings.TrimPrefix(filepath.ToSlash(path), strings.TrimSuffix(peopleDirectory, "/")+"/")
	parts := strings.Split(relative, "/")
	if len(parts) < 2 {
		return []string{"Ungrouped"}, 5, ""
	}
	group := strings.TrimSpace(parts[0])
	importance := 5
	if match := legacyGroupPrefixPattern.FindStringSubmatch(group); len(match) == 3 {
		if rank, err := strconv.Atoi(match[1]); err == nil {
			importance = clampImportance(11 - rank)
		}
		group = strings.TrimSpace(match[2])
	}
	company := ""
	if strings.Contains(strings.ToLower(group), "коллег") && len(parts) > 2 {
		candidate := strings.TrimSpace(parts[1])
		if candidate != "" && !strings.EqualFold(candidate, "img") {
			company = candidate
		}
	}
	return []string{group}, importance, company
}

func normalizePersonPhotoPath(personPath, peopleDirectory, photo string) string {
	photo = strings.TrimSpace(photo)
	if photo == "" || strings.HasPrefix(photo, "http://") || strings.HasPrefix(photo, "https://") || strings.HasPrefix(photo, "data:") {
		return ""
	}
	photo = strings.Trim(photo, "<>")
	if decoded, err := url.PathUnescape(photo); err == nil {
		photo = decoded
	}
	photo = filepath.ToSlash(photo)
	if strings.HasPrefix(photo, strings.TrimSuffix(peopleDirectory, "/")+"/") {
		return filepath.ToSlash(filepath.Clean(photo))
	}
	return filepath.ToSlash(filepath.Clean(filepath.Join(filepath.Dir(personPath), filepath.FromSlash(photo))))
}

func firstMarkdownImage(body string) string {
	if match := markdownImagePattern.FindStringSubmatch(body); len(match) == 2 {
		value := strings.TrimSpace(match[1])
		if fields := strings.Fields(value); len(fields) > 0 {
			return fields[0]
		}
	}
	if match := obsidianImagePattern.FindStringSubmatch(body); len(match) == 2 {
		return strings.TrimSpace(match[1])
	}
	return ""
}

func pathWithinPeopleDirectory(path, directory string) bool {
	normalizedPath := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(path)), "./")
	normalizedDirectory := strings.TrimSuffix(strings.TrimPrefix(filepath.ToSlash(filepath.Clean(directory)), "./"), "/")
	return normalizedPath == normalizedDirectory || strings.HasPrefix(normalizedPath, normalizedDirectory+"/")
}

func safePersonFilename(name string) string {
	name = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', 0:
			return '-'
		default:
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}
	}, strings.TrimSpace(name))
	name = strings.Join(strings.Fields(name), " ")
	name = strings.Trim(name, ". ")
	if name == "" {
		return "Person"
	}
	return name
}

func safePersonPathPart(value string) string {
	return safePersonFilename(value)
}

func cleanStringList(values []string) []string {
	result := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result
}

func cleanStringMap(values map[string]string) map[string]string {
	result := map[string]string{}
	for key, value := range values {
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if key != "" && value != "" {
			result[key] = value
		}
	}
	return result
}

func clampImportance(value int) int {
	if value < 1 {
		return 5
	}
	if value > 10 {
		return 10
	}
	return value
}

func defaultPersonStatus(value string) string {
	if strings.TrimSpace(value) == "" {
		return "active"
	}
	return strings.TrimSpace(value)
}
