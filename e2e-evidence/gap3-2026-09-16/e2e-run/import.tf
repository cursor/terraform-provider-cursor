resource "cursor_origin_repo_grant" "frank" {
  owner      = "acme"
  repo       = "rocket"
  permission = "read"
  user_email = "frank@acme.com"
}
