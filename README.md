# Terraform Provider for Cursor Automations

Manage Cursor Automations with Terraform or OpenTofu.

This standalone provider exposes Cursor Platform workflows as Terraform resources. It talks to the Cursor Automations API over Connect RPC and uses the `CURSOR_TOKEN` authentication flow described below.

## Installation

Once published, configure the provider from the Terraform Registry:

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

For local development, build the binary with `go build` from this directory and install it using your Terraform/OpenTofu development override configuration.

## Provider Configuration

Use explicit configuration or environment variables:

- `CURSOR_TOKEN` - Cursor API token. Required. Raw `key_` API keys are exchanged for a session token automatically.
- `CURSOR_ENDPOINT` - Cursor API base URL. Optional, defaults to `https://api2.cursor.sh`.

```hcl
provider "cursor" {
  token = var.cursor_token
}
```

## Resource: `cursor_platform_workflow`

```hcl
resource "cursor_platform_workflow" "example_review" {
  name    = "Example review automation"
  scope   = "team"
  enabled = true

  prompt = file("prompt.md")
  model  = var.model

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
        allow_approve         = false
      }
    },
    {
      mcp = {
        server = "example-mcp-server"
      }
    }
  ]
}
```

### Top-level Arguments

- `name` - Display name for the automation.
- `scope` - Ownership scope. Supported values are `user`, `team`, `team_visible`, `team_editable_user`, and `team_editable`. Defaults to `user`.
- `enabled` - Whether the automation is enabled.
- `prompt` - Prompt text that defines what the agent should do.
- `effort_level` - Optional effort level: `standard` or `hard`.
- `model` - Optional model identifier.
- `git_repo` / `git_branch` - Optional repository context for non-git triggers such as cron, Slack, Linear, or Teams.
- `skip_install` - Optional, skips install commands and cloud testing.
- `memory_enabled` - Optional, enables persistent automation memory.

### Triggers

Each `trigger` entry must contain exactly one trigger type:

- `git_pull_request` - Trigger on pull request events.
- `git_push` - Trigger on git push events.
- `cron` - Trigger on a cron schedule.
- `slack` - Trigger on Slack messages. Supports `channel`, `message_contains`, `message_contains_is_regex`, `block_unauthenticated_slack_users`, and the completion reaction settings below.
- `linear` - Trigger on Linear events.
- `webhook` - Trigger on generic webhook POST requests.
- `microsoft_teams` - Trigger on Microsoft Teams messages.
- `microsoft_teams_channel_created` - Trigger when a Teams channel is created.

Example cron trigger:

```hcl
trigger = [
  {
    cron = {
      schedule = "0 9 * * *"
    }
  }
]
```

Example pull request trigger:

```hcl
trigger = [
  {
    git_pull_request = {
      repos            = ["example-org/example-repo"]
      pr_action        = "pushed"
      ignore_draft_prs = true
    }
  }
]
```

For `git_pull_request`, specify either `orgs` or `repos`, not both, in a single trigger. Use separate trigger entries if you need both org-wide and explicit repo coverage.

#### Slack completion reaction

A `slack` trigger can control the emoji reaction Cursor adds to the triggering Slack message when the automation completes successfully:

- `completion_reaction_mode` - `on` (default Cursor reaction), `off` (no reaction), or `custom` (use `completion_reaction_custom_emoji`). Leave unset to use the Cursor default.
- `completion_reaction_custom_emoji` - Custom Slack reaction emoji in `:emoji_name:` form. Required when `completion_reaction_mode` is `custom`, and only valid in that mode.

```hcl
trigger = [
  {
    slack = {
      channel                          = "C0123456789"
      completion_reaction_mode         = "custom"
      completion_reaction_custom_emoji = ":white_check_mark:"
    }
  }
]
```

### Actions

Each `action` entry must contain exactly one action type:

- `pr_comment` - Comment on a pull request.
- `git_pr` - Create a pull request.
- `request_reviewers` - Request pull request reviewers.
- `mcp` - Enable a configured MCP server for the automation run.
- `slack` - Post messages to Slack.
- `read_slack` - Give the agent read-only access to public Slack channels.
- `microsoft_teams` - Post messages to Microsoft Teams.
- `read_microsoft_teams` - Give the agent read-only access to Teams channels.

`pr_comment` supports:

- `allow_inline_comments` - Allows PR review comments on specific diff lines.
- `allow_approve` - Allows the PR comment tool to approve or dismiss approvals.

`mcp.server` references an MCP server stored in the automation owner's Cursor MCP settings. The provider only stores the server name; URL, headers, and OAuth credentials are resolved by Cursor at runtime.

## Data Source: `cursor_platform_workflow`

```hcl
data "cursor_platform_workflow" "existing" {
  id = "2a8efc75-f628-4306-aa8f-36d7e9ae9f3d"
}
```

## Development

```bash
go test ./...
go build -o terraform-provider-cursor
```
