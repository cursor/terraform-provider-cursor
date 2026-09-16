# Terraform Provider for Cursor Automations

Manage [Cursor Automations](https://cursor.com) with Terraform or OpenTofu, and read [Origin](https://cursor.com/docs/origin) repositories from the public Origin API.

This provider exposes Cursor Platform workflows as Terraform resources. It talks to the Cursor Automations API over Connect RPC. Origin repositories are a data source: the public Origin API can create and read repositories, but it has no update or delete operation.

Full documentation for the provider, resources, and data sources lives in [`docs/`](./docs) and on the Terraform Registry.

## Installation

```hcl
terraform {
  required_providers {
    cursor = {
      source  = "cursor/cursor"
      version = "~> 0.1"
    }
  }
}
```

## Provider Configuration

Use explicit configuration or environment variables:

- `CURSOR_TOKEN` - Cursor API token. Required. Raw `key_` and `crsr_` API keys are exchanged for a session token automatically.
- `CURSOR_ENDPOINT` - Cursor API base URL. Optional, defaults to `https://api2.cursor.sh`.
- `CURSOR_TEAM_API_KEY` - Team Admin API key. Optional; needed only for Origin grants keyed by `user_email`.
- `CURSOR_ORGANIZATION_API_KEY` - Organization Admin API key. Optional; needed only for Origin grants keyed by `group_name`.

```hcl
provider "cursor" {
  token = var.cursor_token
}
```

## Example

```hcl
resource "cursor_platform_workflow" "example_review" {
  name    = "Example review automation"
  scope   = "team"
  enabled = true

  prompt = file("prompt.md")

  trigger = [
    {
      git_pull_request = {
        repos            = ["example-org/example-repo"]
        pr_action        = "opened"
        ignore_draft_prs = true
      }
    }
  ]

  action = [
    {
      pr_comment = {
        allow_inline_comments = true
      }
    }
  ]
}
```

Supported triggers: `git_pull_request`, `git_push`, `git_ci_completed`, `cron`, `slack`, `linear`, `webhook`, `microsoft_teams`, and `microsoft_teams_channel_created`.

Supported actions: `pr_comment`, `git_pr`, `request_reviewers`, `mcp`, `slack`, `read_slack`, `microsoft_teams`, and `read_microsoft_teams`.

See [`examples/`](./examples) for more, including the data source and import syntax. The resource and data source docs in [`docs/`](./docs) describe every trigger and action type.

## Origin repositories

Read an existing Origin repository with the same provider token. Look up by owner and name, or by the repository's stable ID:

```hcl
data "cursor_origin_repo" "rocket" {
  owner = "acme"
  name  = "rocket"
}
```

`clone_url`, `default_branch`, `visibility`, and mirror metadata come from `GET /v1/origin/repos/{owner}/{name}` on the public Origin API (`https://api.cursor.com/v1/origin`). A raw `key_` or `crsr_` API key is exchanged for a session token first, the same way Automations calls are authenticated.

Manage a repository ruleset with the same token. Each `rule` block sets exactly one typed block; the block name is the Origin rule type and its snake_case arguments map to the camelCase parameters Origin stores. Update replaces the ruleset, including its rules and bypass actors. `deletion_protection` defaults to true: Terraform will not delete the ruleset, or replace it because `owner` or `repo` changed, until that is set to false and applied. A ruleset with no rules, no included refs, or `enforcement = "disabled"` is rejected unless `allow_unenforced` is true. `origin_role` `repository_write` bypasses every principal with write access and requires `allow_broad_bypass`.

```hcl
resource "cursor_origin_repo_ruleset" "main" {
  owner       = "acme"
  repo        = "rocket"
  name        = "require-review"
  enforcement = "active"
  kind        = "merge_branch"

  included_ref_names = ["refs/heads/main"]

  rule {
    pull_request {
      required_approving_review_count = 1
    }
  }

  rule {
    require_status_checks {
      required_check {
        actor_kind = "app"
        actor_id   = "app_01k2ja2000e0080000000000c1"
        group_key  = "ci"
      }
    }
  }
}
```

Merge rules (`pull_request`, `require_status_checks`, `require_branch_up_to_date`) apply to `kind = "merge_branch"`. Push rules apply to the `push_branch`, `push_tag`, and `push_repository` kinds: `deletion`, `non_fast_forward`, `block_direct_updates`, `block_merges`, and `required_linear_history` take no arguments, for example `rule { deletion {} }`, and `ref_name_pattern { pattern = "refs/tags/v*" }` restricts ref names (set `negate = true` to reject matches instead).

Manage direct access grants with `cursor_origin_repo_grant` (repository, `permission` = `read`, `write`, or `admin`) and `cursor_origin_owner_grant` (namespace, `permission` = `read`, `contributor`, `write`, or `admin`). Name the principal with exactly one of `user_email`, `group_name`, `user = { id = "user_..." }`, `group = { id = "grp_..." }`, or `team_group = { kind = "members" | "admins" }`. `user_email` is resolved through the Team Admin API (`GET https://api.cursor.com/teams/members`, provider `team_api_key` / `CURSOR_TEAM_API_KEY`) and `group_name` through the Organization Admin API (`GET https://api.cursor.com/organizations/groups?name=<group_name>`, exact match, using the group's `publicId`, provider `organization_api_key` / `CURSOR_ORGANIZATION_API_KEY`); both keys are sent as HTTP Basic auth with an empty password. The resolved ID is stored in `user` or `group`, and a grant is identified by that ID and the resource rather than a server ID: create and update both upsert over `POST .../grants`, a changed resolution replaces the grant, and refresh drops a grant from state when the principal no longer appears in the list. Import with `owner/repo:user_email:alice@acme.com`, `owner:group_name:Engineering`, `owner/repo:user:user_01...`, or `owner:team_group:admins`. `cursor_origin_repo_grants` and `cursor_origin_owner_grants` list the direct grants on a resource.

```hcl
resource "cursor_origin_repo_grant" "alice" {
  owner      = "acme"
  repo       = "rocket"
  permission = "write"
  user_email = "alice@acme.com"
}

resource "cursor_origin_repo_grant" "eng" {
  owner      = "acme"
  repo       = "rocket"
  permission = "read"
  group_name = "Engineering"
}
```

## Development

```bash
make build   # go build
make test    # go test ./...
make docs    # regenerate docs/ with tfplugindocs
```

Regenerate `docs/` with `make docs` after changing any schema `Description` or the files under `examples/`.

## License

[Apache-2.0](./LICENSE). "Cursor" is a trademark of Anysphere, Inc.
