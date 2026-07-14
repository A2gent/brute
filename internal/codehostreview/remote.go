package codehostreview

import (
	"fmt"
	"net/url"
	"path"
	"strings"
)

func ParseRepositoryRemote(remote string) (Repository, error) {
	trimmed := strings.TrimSpace(remote)
	if strings.HasPrefix(trimmed, "git@bitbucket.org:") {
		return bitbucketRepositoryFromPath(strings.TrimPrefix(trimmed, "git@bitbucket.org:"))
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return Repository{}, fmt.Errorf("invalid git remote: %w", err)
	}
	if !strings.EqualFold(parsed.Hostname(), "bitbucket.org") {
		return Repository{}, fmt.Errorf("unsupported git remote host %q", parsed.Hostname())
	}
	return bitbucketRepositoryFromPath(parsed.Path)
}

func bitbucketRepositoryFromPath(remotePath string) (Repository, error) {
	cleaned := strings.Trim(strings.TrimSuffix(path.Clean(remotePath), ".git"), "/")
	parts := strings.Split(cleaned, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return Repository{}, fmt.Errorf("bitbucket remote must identify workspace/repository")
	}
	return Repository{Provider: "bitbucket", Owner: parts[0], Name: parts[1]}, nil
}
