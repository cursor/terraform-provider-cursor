resource "cursor_platform_workflow" "example_review" {
  name    = "Example review automation"
  scope   = "team"
  enabled = true

  prompt = file("prompt.md")
  model  = "gpt-5.5"

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
