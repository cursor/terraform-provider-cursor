data "cursor_origin_owner_grants" "acme" {
  owner = "acme"
}

# Grants whose policy is not a public preset show permission = "custom".
output "acme_custom_grants" {
  value = [for g in data.cursor_origin_owner_grants.acme.grants : g.id if g.permission == "custom"]
}
