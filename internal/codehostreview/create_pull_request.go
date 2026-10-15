package codehostreview

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

func (s *Service) CreatePullRequest(ctx context.Context, request CreatePullRequestRequest) (*PullRequest, error) {
	repo, err := validateRepository(request.Repository)
	if err != nil {
		return nil, err
	}
	if repo.Provider != "bitbucket" {
		return nil, fmt.Errorf("unsupported pull request provider %q", repo.Provider)
	}
	source := strings.TrimSpace(request.SourceBranch)
	title := strings.TrimSpace(request.Title)
	if source == "" || title == "" {
		return nil, fmt.Errorf("source_branch and title are required")
	}
	creds, err := s.resolveBitbucketCredentials(repo, request.IntegrationID, request.IntegrationName)
	if err != nil {
		return nil, err
	}
	existing, err := s.findBitbucketPullRequest(ctx, creds, repo, source)
	if err != nil || existing != nil {
		return existing, err
	}

	repoPath := bitbucketRepositoryPath(repo)
	destination := strings.TrimSpace(request.DestinationBranch)
	if destination == "" {
		var repository struct {
			MainBranch struct {
				Name string `json:"name"`
			} `json:"mainbranch"`
		}
		if err := s.bitbucketRequest(ctx, creds, http.MethodGet, repoPath, nil, nil, &repository); err != nil {
			return nil, err
		}
		destination = repository.MainBranch.Name
	}

	var author bitbucketUser
	if err := s.bitbucketRequest(ctx, creds, http.MethodGet, "/2.0/user", nil, nil, &author); err != nil {
		return nil, err
	}
	// Bitbucket's API does not apply default reviewers, and rejects the author as a reviewer.
	reviewers := make([]map[string]string, 0)
	endpoint := repoPath + "/effective-default-reviewers"
	query := url.Values{"pagelen": []string{"100"}}
	for endpoint != "" {
		// Each entry wraps the account: {"user": {...}, "reviewer_type": "project|repository"}.
		var page bitbucketPage[struct {
			User bitbucketUser `json:"user"`
		}]
		if err := s.bitbucketRequest(ctx, creds, http.MethodGet, endpoint, query, nil, &page); err != nil {
			return nil, err
		}
		for _, reviewer := range page.Values {
			if reviewer.User.UUID != author.UUID {
				reviewers = append(reviewers, map[string]string{"uuid": reviewer.User.UUID})
			}
		}
		endpoint, query = absoluteBitbucketNextPath(creds.BaseURL, page.Next), nil
	}

	payload := map[string]interface{}{
		"title":               title,
		"description":         request.Description,
		"source":              map[string]interface{}{"branch": map[string]string{"name": source}},
		"destination":         map[string]interface{}{"branch": map[string]string{"name": destination}},
		"close_source_branch": true,
		"reviewers":           reviewers,
	}
	var response bitbucketPullRequest
	if err := s.bitbucketRequest(ctx, creds, http.MethodPost, repoPath+"/pullrequests", nil, payload, &response); err != nil {
		return nil, err
	}
	return normalizeBitbucketPullRequest(response), nil
}
