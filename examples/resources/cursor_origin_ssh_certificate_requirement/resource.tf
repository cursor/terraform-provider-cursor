# Referencing the authority's namespace adds it first and clears the flag before it is removed.
resource "cursor_origin_ssh_certificate_requirement" "acme" {
  namespace            = cursor_origin_ssh_certificate_authority.production.namespace
  require_certificates = true
  deletion_protection  = true
}
