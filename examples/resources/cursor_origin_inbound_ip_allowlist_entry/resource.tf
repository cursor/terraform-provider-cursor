resource "cursor_origin_inbound_ip_allowlist_entry" "office" {
  namespace           = "acme"
  cidr                = "203.0.113.0/24"
  description         = "Office"
  deletion_protection = true
}

resource "cursor_origin_inbound_ip_allowlist_entry" "vpn" {
  namespace   = "acme"
  cidr        = "2001:db8::/32"
  description = "VPN egress"
  enabled     = false
}
