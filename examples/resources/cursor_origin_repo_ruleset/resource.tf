resource "cursor_origin_repo_ruleset" "main" {
  owner       = "acme"
  repo        = "rocket"
  name        = "require-review"
  description = "Require an approving review and green CI before merging to main."
  enforcement = "active"
  kind        = "merge_branch"

  included_ref_names  = ["refs/heads/main"]
  deletion_protection = true

  rule {
    pull_request {
      required_approving_review_count = 1
    }
  }

  rule {
    require_status_checks {
      required_check {
        name = "ci"
      }
    }
  }

  rule {
    require_branch_up_to_date {}
  }

  bypass_actor {
    bypass_mode = "always"
    user {
      id = "act_01k2ja2000e0080000000000u1"
    }
  }
}

resource "cursor_origin_repo_ruleset" "protect_main" {
  owner       = "acme"
  repo        = "rocket"
  name        = "protect-main"
  enforcement = "active"
  kind        = "push_branch"

  included_ref_names = ["refs/heads/main"]

  rule {
    deletion {}
  }

  rule {
    non_fast_forward {}
  }

  rule {
    block_direct_updates {}
  }
}
