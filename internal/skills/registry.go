package skills

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// RegistrySkill represents a skill from clawhub.ai registry
type RegistrySkill struct {
	Slug        string  `json:"slug"`
	DisplayName string  `json:"displayName"`
	Summary     string  `json:"summary"`
	Version     string  `json:"version"`
	Score       float64 `json:"score"`
	UpdatedAt   int64   `json:"updatedAt"`
	Stats       *struct {
		Downloads       int `json:"downloads"`
		Stars           int `json:"stars"`
		InstallsCurrent int `json:"installsCurrent"`
		InstallsAllTime int `json:"installsAllTime"`
	} `json:"stats,omitempty"`
}

// RegistrySkillDetails represents detailed skill info from /api/v1/skills/{slug}
type RegistrySkillDetails struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"displayName"`
	Summary     string `json:"summary"`
	Tags        struct {
		Latest string `json:"latest"`
	} `json:"tags"`
	Stats struct {
		Downloads       int `json:"downloads"`
		Stars           int `json:"stars"`
		InstallsCurrent int `json:"installsCurrent"`
	} `json:"stats"`
	CreatedAt int64 `json:"createdAt"`
	UpdatedAt int64 `json:"updatedAt"`
}

// SearchResponse represents search results from clawhub.ai
type SearchResponse struct {
	Results []RegistrySkill `json:"results"`
}

// RegistryClient handles communication with clawhub.ai
type RegistryClient struct {
	baseURL    string
	httpClient *http.Client
}

// NewRegistryClient creates a new registry client
func NewRegistryClient(baseURL string) *RegistryClient {
	if baseURL == "" {
		baseURL = "https://clawhub.ai"
	}

	return &RegistryClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// SearchSkills searches for skills on clawhub.ai using /api/v1/search
func (c *RegistryClient) SearchSkills(query string, limit int) (*SearchResponse, error) {
	if limit <= 0 {
		limit = 20
	}

	params := url.Values{}
	params.Add("q", query)
	params.Add("limit", fmt.Sprintf("%d", limit))

	searchURL := fmt.Sprintf("%s/api/v1/search?%s", c.baseURL, params.Encode())

	resp, err := c.httpClient.Get(searchURL)
	if err != nil {
		return nil, fmt.Errorf("failed to search skills: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("search failed: %s (status %d)", string(body), resp.StatusCode)
	}

	var result SearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

// ListSkillsResponse represents the response from /api/v1/skills
type ListSkillsResponse struct {
	Items      []ListSkillItem `json:"items"`
	NextCursor *string         `json:"nextCursor"`
}

// ListSkillItem represents a single skill in the list response
type ListSkillItem struct {
	Slug        string            `json:"slug"`
	DisplayName string            `json:"displayName"`
	Summary     string            `json:"summary"`
	Tags        map[string]string `json:"tags"`
	Stats       struct {
		Downloads       int `json:"downloads"`
		Stars           int `json:"stars"`
		InstallsCurrent int `json:"installsCurrent"`
		InstallsAllTime int `json:"installsAllTime"`
	} `json:"stats"`
	CreatedAt     int64 `json:"createdAt"`
	UpdatedAt     int64 `json:"updatedAt"`
	LatestVersion *struct {
		Version string `json:"version"`
	} `json:"latestVersion"`
}

// ListSkills lists skills with sorting and filtering using /api/v1/skills
func (c *RegistryClient) ListSkills(limit int, sort string) (*ListSkillsResponse, error) {
	if limit <= 0 {
		limit = 20
	}
	if sort == "" {
		sort = "installsCurrent" // Default to most installed
	}

	params := url.Values{}
	params.Add("limit", fmt.Sprintf("%d", limit))
	params.Add("sort", sort)

	listURL := fmt.Sprintf("%s/api/v1/skills?%s", c.baseURL, params.Encode())

	resp, err := c.httpClient.Get(listURL)
	if err != nil {
		return nil, fmt.Errorf("failed to list skills: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list failed: %s (status %d)", string(body), resp.StatusCode)
	}

	var result ListSkillsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

// GetSkill retrieves a specific skill details by slug using /api/v1/skills/{slug}
func (c *RegistryClient) GetSkill(slug string) (*RegistrySkillDetails, error) {
	getURL := fmt.Sprintf("%s/api/v1/skills/%s", c.baseURL, slug)

	resp, err := c.httpClient.Get(getURL)
	if err != nil {
		return nil, fmt.Errorf("failed to get skill: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get skill failed: %s (status %d)", string(body), resp.StatusCode)
	}

	var response struct {
		Skill RegistrySkillDetails `json:"skill"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, fmt.Errorf("failed to decode skill: %w", err)
	}

	return &response.Skill, nil
}

// DownloadSkill downloads and installs a skill from clawhub.ai using /api/v1/download
func (c *RegistryClient) DownloadSkill(slug, targetDir string) error {
	// Download skill ZIP using /api/v1/download
	downloadURL := fmt.Sprintf("%s/api/v1/download?slug=%s", c.baseURL, url.QueryEscape(slug))

	resp, err := c.httpClient.Get(downloadURL)
	if err != nil {
		return fmt.Errorf("failed to download skill: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)

		// Special handling for rate limit
		if resp.StatusCode == http.StatusTooManyRequests {
			retryAfter := resp.Header.Get("Retry-After")
			if retryAfter != "" {
				return fmt.Errorf("rate limit exceeded - please try again in %s seconds", retryAfter)
			}
			return fmt.Errorf("rate limit exceeded - please wait a moment and try again")
		}

		return fmt.Errorf("download failed: %s (status %d)", string(body), resp.StatusCode)
	}

	// Read ZIP into memory
	zipData, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read download: %w", err)
	}

	// Open ZIP from memory
	zipReader, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		return fmt.Errorf("failed to open ZIP: %w", err)
	}

	// Extract ZIP to target directory
	skillDir := filepath.Join(targetDir, slug)
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		return fmt.Errorf("failed to create skill directory: %w", err)
	}

	for _, file := range zipReader.File {
		// Open file in ZIP
		rc, err := file.Open()
		if err != nil {
			return fmt.Errorf("failed to open file in ZIP: %w", err)
		}

		// Create destination path
		destPath := filepath.Join(skillDir, file.Name)

		// Create directories if needed
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(destPath, file.Mode()); err != nil {
				rc.Close()
				return fmt.Errorf("failed to create directory: %w", err)
			}
			rc.Close()
			continue
		}

		// Create file
		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			rc.Close()
			return fmt.Errorf("failed to create parent directory: %w", err)
		}

		outFile, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, file.Mode())
		if err != nil {
			rc.Close()
			return fmt.Errorf("failed to create file: %w", err)
		}

		_, err = io.Copy(outFile, rc)
		outFile.Close()
		rc.Close()

		if err != nil {
			return fmt.Errorf("failed to extract file: %w", err)
		}
	}

	return nil
}

// InstallSkill downloads and installs a skill
func InstallSkill(registryURL, slug, targetDir string) (*Skill, error) {
	client := NewRegistryClient(registryURL)

	// Download skill ZIP and extract it
	if err := client.DownloadSkill(slug, targetDir); err != nil {
		return nil, err
	}

	// Load the installed skill
	skillPath := filepath.Join(targetDir, slug, "SKILL.md")
	installedSkill, err := LoadSkill(skillPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load installed skill: %w", err)
	}

	return installedSkill, nil
}
