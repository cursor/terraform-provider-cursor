# Terraform Provider for Cursor Automations

Manage [Cursor Automations](https://cursor.com) with Terraform or OpenTofu.

This provider exposes Cursor Platform workflows as Terraform resources. It talks to the Cursor Automations API over Connect RPC.

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

## Development

```bash
make build   # go build
make test    # go test ./...
make docs    # regenerate docs/ with tfplugindocs
```

Regenerate `docs/` with `make docs` after changing any schema `Description` or the files under `examples/`.

## License

[Apache-2.0](./LICENSE). "Cursor" is a trademark of Anysphere, Inc.
