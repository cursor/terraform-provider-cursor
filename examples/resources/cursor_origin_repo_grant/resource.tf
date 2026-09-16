# Principals by email or group name resolve through the Team and Organization
# Admin APIs; set provider team_api_key and organization_api_key.
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

# Stable IDs work without the Admin API keys.
resource "cursor_origin_repo_grant" "release_bot" {
  owner      = "acme"
  repo       = "rocket"
  permission = "write"

  user = {
    id = "user_01k2ja2000e0080000000000u1"
  }
}

resource "cursor_origin_repo_grant" "platform_admins" {
  owner      = "acme"
  repo       = "rocket"
  permission = "admin"

  group = {
    id = "grp_01k2ja2000e0080000000000g7"
  }
}

# Team floor: every member of the owning team can read the repository.
resource "cursor_origin_repo_grant" "team_readers" {
  owner      = "acme"
  repo       = "rocket"
  permission = "read"

  team_group = {
    kind = "members"
  }
}
