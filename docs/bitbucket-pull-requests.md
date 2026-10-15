# Bitbucket pull requests

Agents can use `bitbucket_create_pull_request` from the `git_review` tool group. The Bitbucket integration also exposes `code_review` for reading comments and replying to review threads.

## Integration permissions

Configure an enabled Bitbucket integration with the account email and API token. PR creation needs **write:pullrequest** scope (`write:pullrequest:bitbucket` on scoped Atlassian API tokens), in addition to read access for pull requests, repositories/default reviewers, and the authenticated user (`/2.0/user`). A successful integration connection test only verifies the user endpoint, not PR write permission.

## Usage

Push the source branch first, then call:

```json
{
  "source_branch": "feature/example",
  "title": "Add example",
  "description": "Summary and verification steps."
}
```

- `source_branch` and `title` are required.
- `owner` (workspace) and `repository` default individually from the session project's `origin` git remote. HTTPS, SCP-style SSH, `ssh://` and `ssh+git://user@bitbucket.org/workspace/repository.git` remotes are supported. Pass both explicitly for a different repository or a project without a Bitbucket origin.
- `destination_branch` defaults to the repository's configured main branch, not a hard-coded `main` or `master`.
- `integration_id` or `integration_name` selects credentials when needed; otherwise the existing workspace-based selection applies.
- An existing open PR for the source branch is returned unchanged. Retrying does not update its title, description, destination or reviewers.
- New PRs include all effective default reviewers except the authenticated author. Bitbucket's create API does not apply default reviewers automatically.
- `close_source_branch` is always `true`: Bitbucket deletes the source branch when the PR is merged, not when this tool runs.
- The result includes the PR ID, HTML `url`, branches, state and reviewer names.

## Verification

```sh
go test ./internal/codehostreview/... ./internal/tools/integrationtools/... ./internal/http/...
```

For a live smoke test, use an explicitly approved sandbox repository with a pushed test branch and configured default reviewers:

1. Call the tool without `destination_branch` and check the returned HTML URL, target branch and reviewers. Confirm the author is excluded.
2. Call it again with the same source branch and confirm the same PR ID is returned.
3. Decline the test PR in Bitbucket (or `POST /2.0/repositories/{workspace}/{repository}/pullrequests/{id}/decline` using the same integration credentials). Confirm its state is `DECLINED`. Do not merge it.

A live smoke test must use the native integration service, not JavaScript executed in a logged-in browser tab.
