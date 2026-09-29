data "cursor_origin_inbound_ip_allowlist_entries" "acme" {
  namespace = "acme"
}

output "acme_admitted_cidrs" {
  value = [for entry in data.cursor_origin_inbound_ip_allowlist_entries.acme.entries : entry.cidr if entry.enabled]
}
