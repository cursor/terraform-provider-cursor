# Referencing the authority's owner adds it first and clears the flag before it is removed.
resource "cursor_origin_ssh_certificate_requirement" "acme" {
  owner                = cursor_origin_ssh_certificate_authority.production.owner
  require_certificates = true
  deletion_protection  = true
}
