data "cursor_origin_repo" "rocket" {
  owner = "acme"
  name  = "rocket"
}

# The public Origin API can also address a repository by its stable ID.
data "cursor_origin_repo" "by_id" {
  id = "repo_01k2ja2000e0080000000000q4"
}
