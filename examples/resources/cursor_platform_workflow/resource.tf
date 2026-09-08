resource "cursor_platform_workflow" "example_review" {
  name        = "Example review automation"
  description = "Reviews pull requests and posts inline comments."
  scope       = "team"
  enabled     = true

  prompt = file("prompt.md")
  model  = "gpt-5.5"

  private_worker = {
    labels = {
      repo = "example-org/example-repo"
      pool = "example-reviewers"
    }
  }

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
      manage_check_run = {}
    },
    {
      resolve_review_threads = {}
    },
    {
      mcp = {
        server = "example-mcp-server"
      }
    }
  ]
}

resource "cursor_platform_workflow" "example_slack_triage" {
  name   = "Triage Slack reactions"
  prompt = "Investigate the message that received the reaction and reply in thread."

  # Structured model choice. Auto tiers all share the "auto-smart" model ID and
  # differ by optimize_for: "cost" (Auto Cost), "balanced" (Auto Balance) or
  # "intelligence". Leave `model` unset: the server derives it from the selection.
  model_selection = {
    model_id = "auto-smart"
    parameters = [
      { id = "optimize_for", value = "cost" }
    ]
  }

  git_repo               = "github.com/example-org/example-repo"
  disabled_default_tools = ["open_git_pr"]

  trigger = [
    {
      slack_reaction_added = {
        channel    = "C0123456789"
        emoji_name = "eyes"
      }
    },
    {
      slack_mention = {
        channel = "C0123456789"
      }
    }
  ]

  action = [
    {
      slack = {
        channel = "C0123456789"
      }
    }
  ]
}

resource "cursor_platform_workflow" "example_incidents" {
  name   = "Incident responder"
  prompt = "Summarise the incident and open a PR with a proposed fix."

  # Auto Balance.
  model_selection = {
    model_id = "auto-smart"
    parameters = [
      { id = "optimize_for", value = "balanced" }
    ]
  }

  git_repo = "github.com/example-org/example-repo"

  trigger = [
    {
      pagerduty = {
        incident_triggered = {}
        service_ids        = ["PABC123"]
      }
    },
    {
      sentry = {
        issue_created = {}
        project_ids   = ["123456"]
      }
    }
  ]

  action = [
    {
      git_pr = {}
    }
  ]
}
