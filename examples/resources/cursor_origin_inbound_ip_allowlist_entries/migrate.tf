# Terraform 1.7 or later. Drop the per-entry resources from state without
# deleting the entries, then adopt the whole set with an import block.
removed {
  from = cursor_origin_inbound_ip_allowlist_entry.office

  lifecycle {
    destroy = false
  }
}

# A for_each or count resource is removed with one block for all its instances.
removed {
  from = cursor_origin_inbound_ip_allowlist_entry.egress

  lifecycle {
    destroy = false
  }
}

import {
  to = cursor_origin_inbound_ip_allowlist_entries.acme
  id = "acme"
}

resource "cursor_origin_inbound_ip_allowlist_entries" "acme" {
  namespace = "acme"

  entry {
    cidr        = "203.0.113.0/24"
    description = "Office"
  }

  entry {
    cidr        = "198.51.100.7"
    description = "CI runner"
  }
}
