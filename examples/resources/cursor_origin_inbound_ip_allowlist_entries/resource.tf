resource "cursor_origin_inbound_ip_allowlist_entries" "acme" {
  namespace = "acme"

  entry {
    cidr        = "203.0.113.0/24"
    description = "Office"
  }

  entry {
    cidr        = "2001:db8::/32"
    description = "VPN egress"
    enabled     = false
  }
}

# Build the set from a map with a dynamic block.
locals {
  egress = {
    "198.51.100.7"    = "CI runner"
    "198.51.100.0/28" = "Build farm"
  }
}

resource "cursor_origin_inbound_ip_allowlist_entries" "rocket" {
  namespace = "rocket"

  dynamic "entry" {
    for_each = local.egress
    content {
      cidr        = entry.key
      description = entry.value
    }
  }
}

resource "cursor_origin_inbound_ip_allowlist" "acme" {
  namespace  = "acme"
  enabled    = true
  depends_on = [cursor_origin_inbound_ip_allowlist_entries.acme]
}
