package codehostreview

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/storage"
)

const (
	defaultBitbucketAPIBaseURL = "https://api.bitbucket.org"
	bitbucketResponseLimit     = 8 * 1024 * 1024
)

type Service struct {
	store  storage.Store
	client *http.Client
}

type bitbucketCredentials struct {
	Email   string
	Token   string
	BaseURL string
	Source  string
}

type bitbucketPage[T any] struct {
	Values []T    `json:"values"`
	Next   string `json:"next"`
}

type bitbucketPullRequest struct {
	ID     int64  `json:"id"`
	Title  string `json:"title"`
	State  string `json:"state"`
	Source struct {
		Branch struct {
			Name string `json:"name"`
		} `json:"branch"`
	} `json:"source"`
	Destination struct {
		Branch struct {
			Name string `json:"name"`
		} `json:"branch"`
	} `json:"destination"`
	Links bitbucketLinks `json:"links"`
}

type bitbucketComment struct {
	ID     int64 `json:"id"`
	Parent *struct {
		ID int64 `json:"id"`
	} `json:"parent"`
	Content struct {
		Raw string `json:"raw"`
	} `json:"content"`
	User struct {
		DisplayName string `json:"display_name"`
		Nickname    string `json:"nickname"`
		Links       struct {
			Avatar bitbucketLink `json:"avatar"`
		} `json:"links"`
	} `json:"user"`
	CreatedOn time.Time `json:"created_on"`
	UpdatedOn time.Time `json:"updated_on"`
	Inline    *struct {
		From      int    `json:"from"`
		To        int    `json:"to"`
		StartFrom int    `json:"start_from"`
		StartTo   int    `json:"start_to"`
		Path      string `json:"path"`
	} `json:"inline"`
	Deleted    bool            `json:"deleted"`
	Resolution json.RawMessage `json:"resolution"`
	Links      bitbucketLinks  `json:"links"`
}

type bitbucketLink struct {
	Href string `json:"href"`
}
type bitbucketLinks struct {
	HTML bitbucketLink `json:"html"`
}

func NewService(store storage.Store, client *http.Client) *Service {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Service{store: store, client: client}
}

func (s *Service) GetReview(ctx context.Context, request GetReviewRequest) (Review, error) {
	repo, err := validateRepository(request.Repository)
	if err != nil {
		return Review{}, err
	}
	if repo.Provider != "bitbucket" {
		return Review{}, fmt.Errorf("unsupported code review provider %q", repo.Provider)
	}
	creds, err := s.resolveBitbucketCredentials(repo, request.IntegrationID, request.IntegrationName)
	if err != nil {
		return Review{}, err
	}
	pullRequestID := strings.TrimSpace(request.PullRequestID)
	var pullRequest *PullRequest
	if pullRequestID == "" {
		pullRequest, err = s.findBitbucketPullRequest(ctx, creds, repo, request.Branch)
		if err != nil {
			return Review{}, err
		}
		if pullRequest == nil {
			return Review{Provider: repo.Provider, Repository: repo, Comments: []Comment{}}, nil
		}
		pullRequestID = pullRequest.ID
	}
	comments, err := s.listBitbucketComments(ctx, creds, repo, pullRequestID)
	if err != nil {
		return Review{}, err
	}
	if pullRequest == nil {
		pullRequest = &PullRequest{ID: pullRequestID}
	}
	return Review{Provider: repo.Provider, Repository: repo, PullRequest: pullRequest, Comments: comments}, nil
}

func (s *Service) Reply(ctx context.Context, request ReplyRequest) (Comment, error) {
	repo, err := validateRepository(request.Repository)
	if err != nil {
		return Comment{}, err
	}
	if repo.Provider != "bitbucket" {
		return Comment{}, fmt.Errorf("unsupported code review provider %q", repo.Provider)
	}
	pullRequestID := strings.TrimSpace(request.PullRequestID)
	commentID := strings.TrimSpace(request.CommentID)
	body := strings.TrimSpace(request.Body)
	if pullRequestID == "" || commentID == "" || body == "" {
		return Comment{}, fmt.Errorf("pull_request_id, comment_id, and body are required")
	}
	parentID, err := strconv.ParseInt(commentID, 10, 64)
	if err != nil || parentID <= 0 {
		return Comment{}, fmt.Errorf("bitbucket comment_id must be a positive integer")
	}
	creds, err := s.resolveBitbucketCredentials(repo, request.IntegrationID, request.IntegrationName)
	if err != nil {
		return Comment{}, err
	}
	payload := map[string]interface{}{
		"content": map[string]string{"raw": body},
		"parent":  map[string]int64{"id": parentID},
	}
	var response bitbucketComment
	endpoint := bitbucketRepositoryPath(repo) + "/pullrequests/" + url.PathEscape(pullRequestID) + "/comments"
	if err := s.bitbucketRequest(ctx, creds, http.MethodPost, endpoint, nil, payload, &response); err != nil {
		return Comment{}, err
	}
	return normalizeBitbucketComment(response), nil
}

func (s *Service) findBitbucketPullRequest(ctx context.Context, creds bitbucketCredentials, repo Repository, branch string) (*PullRequest, error) {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return nil, fmt.Errorf("branch is required when pull_request_id is not provided")
	}
	query := url.Values{}
	query.Set("state", "OPEN")
	query.Set("q", fmt.Sprintf("source.branch.name=%s", strconv.Quote(branch)))
	query.Set("pagelen", "50")
	var page bitbucketPage[bitbucketPullRequest]
	if err := s.bitbucketRequest(ctx, creds, http.MethodGet, bitbucketRepositoryPath(repo)+"/pullrequests", query, nil, &page); err != nil {
		return nil, err
	}
	for _, item := range page.Values {
		if item.Source.Branch.Name == branch {
			return normalizeBitbucketPullRequest(item), nil
		}
	}
	return nil, nil
}

func (s *Service) listBitbucketComments(ctx context.Context, creds bitbucketCredentials, repo Repository, pullRequestID string) ([]Comment, error) {
	endpoint := bitbucketRepositoryPath(repo) + "/pullrequests/" + url.PathEscape(pullRequestID) + "/comments"
	query := url.Values{"pagelen": []string{"100"}}
	comments := make([]Comment, 0)
	for endpoint != "" {
		var page bitbucketPage[bitbucketComment]
		if err := s.bitbucketRequest(ctx, creds, http.MethodGet, endpoint, query, nil, &page); err != nil {
			return nil, err
		}
		for _, item := range page.Values {
			comments = append(comments, normalizeBitbucketComment(item))
		}
		endpoint, query = absoluteBitbucketNextPath(creds.BaseURL, page.Next), nil
	}
	sort.SliceStable(comments, func(i, j int) bool { return comments[i].CreatedAt.Before(comments[j].CreatedAt) })
	return comments, nil
}

func (s *Service) bitbucketRequest(ctx context.Context, creds bitbucketCredentials, method string, endpoint string, query url.Values, payload interface{}, target interface{}) error {
	requestURL := strings.TrimRight(creds.BaseURL, "/") + endpoint
	if len(query) > 0 {
		requestURL += "?" + query.Encode()
	}
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, requestURL, body)
	if err != nil {
		return fmt.Errorf("failed to create Bitbucket request: %w", err)
	}
	req.SetBasicAuth(creds.Email, creds.Token)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("Bitbucket request failed: %w", err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, bitbucketResponseLimit))
	if err != nil {
		return fmt.Errorf("failed to read Bitbucket response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := strings.TrimSpace(string(responseBody))
		if message == "" {
			message = resp.Status
		}
		return fmt.Errorf("Bitbucket API error (status %d): %s", resp.StatusCode, message)
	}
	if target == nil || len(responseBody) == 0 {
		return nil
	}
	if err := json.Unmarshal(responseBody, target); err != nil {
		return fmt.Errorf("failed to decode Bitbucket response: %w", err)
	}
	return nil
}

func (s *Service) resolveBitbucketCredentials(repo Repository, integrationID, integrationName string) (bitbucketCredentials, error) {
	if s.store == nil {
		return bitbucketCredentials{}, fmt.Errorf("bitbucket integration is required; configure one in Integrations")
	}
	all, err := s.store.ListIntegrations()
	if err != nil {
		return bitbucketCredentials{}, fmt.Errorf("failed to load integrations: %w", err)
	}
	candidates := make([]*storage.Integration, 0)
	for _, item := range all {
		if item != nil && item.Provider == "bitbucket" && item.Enabled {
			candidates = append(candidates, item)
		}
	}
	selected, err := selectBitbucketIntegration(candidates, repo.Owner, integrationID, integrationName)
	if err != nil {
		return bitbucketCredentials{}, err
	}
	credentials := bitbucketCredentials{
		Email: strings.TrimSpace(selected.Config["email"]), Token: strings.TrimSpace(selected.Config["api_token"]),
		BaseURL: strings.TrimRight(strings.TrimSpace(selected.Config["api_base_url"]), "/"), Source: selected.Name,
	}
	if credentials.BaseURL == "" {
		credentials.BaseURL = defaultBitbucketAPIBaseURL
	}
	parsed, parseErr := url.Parse(credentials.BaseURL)
	if credentials.Email == "" || credentials.Token == "" {
		return bitbucketCredentials{}, fmt.Errorf("selected bitbucket integration requires email and api_token")
	}
	if parseErr != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return bitbucketCredentials{}, fmt.Errorf("bitbucket api_base_url must be an absolute http or https URL")
	}
	return credentials, nil
}

func selectBitbucketIntegration(candidates []*storage.Integration, workspace, integrationID, integrationName string) (*storage.Integration, error) {
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no enabled bitbucket integrations found")
	}
	if id := strings.TrimSpace(integrationID); id != "" {
		for _, item := range candidates {
			if item.ID == id {
				return item, nil
			}
		}
		return nil, fmt.Errorf("bitbucket integration with id %q not found or disabled", id)
	}
	if name := strings.TrimSpace(integrationName); name != "" {
		for _, item := range candidates {
			if strings.EqualFold(strings.TrimSpace(item.Name), name) {
				return item, nil
			}
		}
		return nil, fmt.Errorf("bitbucket integration named %q not found", name)
	}
	workspaceMatches := make([]*storage.Integration, 0)
	for _, item := range candidates {
		if strings.EqualFold(strings.TrimSpace(item.Config["workspace"]), workspace) {
			workspaceMatches = append(workspaceMatches, item)
		}
	}
	if len(workspaceMatches) == 1 {
		return workspaceMatches[0], nil
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	return nil, fmt.Errorf("multiple bitbucket integrations are enabled; configure workspace or pass integration_id")
}

func validateRepository(repo Repository) (Repository, error) {
	repo.Provider = strings.ToLower(strings.TrimSpace(repo.Provider))
	repo.Owner = strings.TrimSpace(repo.Owner)
	repo.Name = strings.TrimSuffix(strings.TrimSpace(repo.Name), ".git")
	if repo.Provider == "" || repo.Owner == "" || repo.Name == "" {
		return Repository{}, fmt.Errorf("provider, owner, and repository are required")
	}
	return repo, nil
}

func bitbucketRepositoryPath(repo Repository) string {
	return "/2.0/repositories/" + url.PathEscape(repo.Owner) + "/" + url.PathEscape(repo.Name)
}

func absoluteBitbucketNextPath(baseURL, next string) string {
	if strings.TrimSpace(next) == "" {
		return ""
	}
	parsed, err := url.Parse(next)
	if err != nil {
		return ""
	}
	base, err := url.Parse(baseURL)
	if err != nil || !strings.EqualFold(parsed.Host, base.Host) {
		return ""
	}
	if parsed.RawQuery != "" {
		return parsed.EscapedPath() + "?" + parsed.RawQuery
	}
	return parsed.EscapedPath()
}

func normalizeBitbucketPullRequest(item bitbucketPullRequest) *PullRequest {
	return &PullRequest{ID: strconv.FormatInt(item.ID, 10), Title: item.Title, State: item.State, SourceBranch: item.Source.Branch.Name, TargetBranch: item.Destination.Branch.Name, URL: item.Links.HTML.Href}
}

func normalizeBitbucketComment(item bitbucketComment) Comment {
	comment := Comment{ID: strconv.FormatInt(item.ID, 10), Body: item.Content.Raw, Author: item.User.DisplayName, AvatarURL: item.User.Links.Avatar.Href, CreatedAt: item.CreatedOn, UpdatedAt: item.UpdatedOn, Deleted: item.Deleted, Resolved: len(item.Resolution) > 0 && string(item.Resolution) != "null", URL: item.Links.HTML.Href}
	if comment.Author == "" {
		comment.Author = item.User.Nickname
	}
	if item.Parent != nil {
		comment.ParentID = strconv.FormatInt(item.Parent.ID, 10)
	}
	if item.Inline != nil {
		comment.FilePath = item.Inline.Path
		if item.Inline.To > 0 {
			comment.Side, comment.LineNumber, comment.StartLine = "additions", item.Inline.To, item.Inline.StartTo
		} else if item.Inline.From > 0 {
			comment.Side, comment.LineNumber, comment.StartLine = "deletions", item.Inline.From, item.Inline.StartFrom
		}
	}
	return comment
}
