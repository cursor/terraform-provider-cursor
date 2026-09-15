resource "cursor_origin_owner_grant" "security" {
  owner      = "acme"
  permission = "admin"
  group_name = "Security"
}

resource "cursor_origin_owner_grant" "bob" {
  owner      = "acme"
  permission = "read"
  user_email = "bob@acme.com"
}

# Stable IDs work without the Admin API keys.
resource "cursor_origin_owner_grant" "platform" {
  owner      = "acme"
  permission = "write"

  group = {
    id = "grp_01k2ja2000e0080000000000g7"
  }
}

# Team floor: every member of the team can open pull requests in any acme repository.
resource "cursor_origin_owner_grant" "team_contributors" {
  owner      = "acme"
  permission = "contributor"

  team_group = {
    kind = "members"
  }
}
