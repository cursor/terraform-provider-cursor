# depends_on adds the office entry before enforcement starts and stops enforcement before the entry is removed.
resource "cursor_origin_inbound_ip_allowlist" "acme" {
  namespace           = "acme"
  enabled             = true
  deletion_protection = true

  depends_on = [cursor_origin_inbound_ip_allowlist_entry.office]
}
