terraform {
  required_providers {
    cursor = {
      source = "cursor/cursor"
    }
  }
}

# token, team_api_key and organization_api_key come from CURSOR_TOKEN,
# CURSOR_TEAM_API_KEY and CURSOR_ORGANIZATION_API_KEY (fake values, mock only).
provider "cursor" {}

variable "alice_email" {
  type    = string
  default = "alice@acme.com"
}

variable "alice_permission" {
  type    = string
  default = "write"
}

variable "late_email" {
  type    = string
  default = "bob@acme.com"
}

variable "ruleset_deletion_protection" {
  type    = bool
  default = true
}

# ---- PR #12: repo data source + ruleset -------------------------------------------------

data "cursor_origin_repo" "rocket" {
  owner = "acme"
  name  = "rocket"
}

resource "cursor_origin_repo_ruleset" "main" {
  owner       = "acme"
  repo        = data.cursor_origin_repo.rocket.name
  name        = "require-review"
  description = "e2e: review + CI before merge"
  enforcement = "active"
  kind        = "merge_branch"

  included_ref_names  = ["refs/heads/main"]
  deletion_protection = var.ruleset_deletion_protection

  rule {
    pull_request {
      required_approving_review_count = 1
      dismiss_stale_reviews_on_push   = true
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

  bypass_actor {
    bypass_mode = "always"
    origin_role {
      role = "repository_admin"
    }
  }
}

# ---- PR #13: grants ----------------------------------------------------------------------

# user_email -> Team Admin API -> user_ id on the Origin wire
resource "cursor_origin_repo_grant" "alice" {
  owner      = "acme"
  repo       = "rocket"
  permission = var.alice_permission
  user_email = var.alice_email
}

# group_name -> Organization Admin API ?name= -> publicId grp_ on the Origin wire
resource "cursor_origin_repo_grant" "eng" {
  owner      = "acme"
  repo       = "rocket"
  permission = "read"
  group_name = "Engineering"
}

# Namespace grant by group_name, contributor -> PERMISSION_CONTRIBUTOR on the wire
resource "cursor_origin_owner_grant" "eng_ns" {
  owner      = "acme"
  permission = "contributor"
  group_name = "Engineering"
}

resource "cursor_origin_owner_grant" "security" {
  owner      = "acme"
  permission = "admin"
  group_name = "Security"
}

# Stable ID principal: no Admin API call may happen for this one
resource "cursor_origin_repo_grant" "carol_by_id" {
  owner      = "acme"
  repo       = "rocket"
  permission = "write"

  user = {
    id = "user_01k2ja2000e0080000000000c3"
  }
}

# Team floor principal
resource "cursor_origin_owner_grant" "team_admins" {
  owner      = "acme"
  permission = "admin"

  team_group = {
    kind = "admins"
  }
}

# user_email that is UNKNOWN at plan time: terraform_data.output is only known after apply.
# Exercises the unknown-at-plan path (user / id must be unknown in the plan, resolution happens at apply;
# on a later input change the grant must be replaced and must not reuse the stale prior user id).
resource "terraform_data" "late_email" {
  input = var.late_email
}

resource "cursor_origin_repo_grant" "late" {
  owner      = "acme"
  repo       = "rocket"
  permission = "read"
  user_email = terraform_data.late_email.output
}

# ---- read back -----------------------------------------------------------------------------

data "cursor_origin_repo_grants" "rocket" {
  owner = "acme"
  repo  = "rocket"
  depends_on = [
    cursor_origin_repo_grant.alice,
    cursor_origin_repo_grant.eng,
    cursor_origin_repo_grant.carol_by_id,
    cursor_origin_repo_grant.late,
  ]
}

data "cursor_origin_owner_grants" "acme" {
  owner = "acme"
  depends_on = [
    cursor_origin_owner_grant.eng_ns,
    cursor_origin_owner_grant.security,
    cursor_origin_owner_grant.team_admins,
  ]
}

output "repo_id" {
  value = data.cursor_origin_repo.rocket.id
}

output "ruleset_id" {
  value = cursor_origin_repo_ruleset.main.id
}

output "alice" {
  value = { id = cursor_origin_repo_grant.alice.id, user_email = cursor_origin_repo_grant.alice.user_email, user = cursor_origin_repo_grant.alice.user }
}

output "eng" {
  value = { id = cursor_origin_repo_grant.eng.id, group_name = cursor_origin_repo_grant.eng.group_name, group = cursor_origin_repo_grant.eng.group }
}

output "eng_ns" {
  value = { id = cursor_origin_owner_grant.eng_ns.id, permission = cursor_origin_owner_grant.eng_ns.permission, group = cursor_origin_owner_grant.eng_ns.group }
}

output "late" {
  value = { id = cursor_origin_repo_grant.late.id, user_email = cursor_origin_repo_grant.late.user_email, user = cursor_origin_repo_grant.late.user }
}

output "repo_grants" {
  value = data.cursor_origin_repo_grants.rocket.grants
}

output "owner_grants" {
  value = data.cursor_origin_owner_grants.acme.grants
}
