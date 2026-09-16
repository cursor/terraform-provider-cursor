data "cursor_origin_repo_grants" "rocket" {
  owner = "acme"
  repo  = "rocket"
}

output "rocket_admins" {
  value = [for g in data.cursor_origin_repo_grants.rocket.grants : g.id if g.permission == "admin"]
}
