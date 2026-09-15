resource "cursor_origin_repo_ruleset" "main" {
  owner       = "acme"
  repo        = "rocket"
  name        = "require-review"
  description = "Require an approving review before merging to main."
  enforcement = "active"
  kind        = "merge_branch"

  included_ref_names = ["refs/heads/main"]
  deletion_protection = true

  rule {
    rule_type = "pull_request"
    parameters = jsonencode({
      requiredApprovingReviewCount = 1
    })
  }

  bypass_actor {
    bypass_mode = "always"
    user {
      id = "act_01k2ja2000e0080000000000u1"
    }
  }
}
