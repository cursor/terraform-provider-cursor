---
page_title: "cursor_origin_inbound_ip_allowlist_entries Resource - cursor"
subcategory: ""
description: |-
  Manages the complete set of entries on an Origin namespace's inbound IP allowlist with one ReplaceInboundIpAllowlistEntries call per create, update, or destroy, however many entries change. Entries not listed here are removed. Do not use this resource together with cursor_origin_inbound_ip_allowlist_entry for the same namespace; the two will overwrite each other's changes. Whether the list is enforced stays with cursor_origin_inbound_ip_allowlist. A namespace lists at most 1000 entries and cannot list the same cidr spelling twice. Origin rejects a set that would exclude the caller's own address from an enforced list, so keep an entry that admits the address Terraform calls from. Updates send the etag from the last read, so Origin rejects the replace if the entries changed since then. deletion_protection defaults to true, so Terraform will not remove every entry or move them to another namespace until that is set to false and applied.
---

# cursor_origin_inbound_ip_allowlist_entries (Resource)

Manages the complete set of entries on an Origin namespace's inbound IP allowlist with one ReplaceInboundIpAllowlistEntries call per create, update, or destroy, however many entries change. Entries not listed here are removed. Do not use this resource together with cursor_origin_inbound_ip_allowlist_entry for the same namespace; the two will overwrite each other's changes. Whether the list is enforced stays with cursor_origin_inbound_ip_allowlist. A namespace lists at most 1000 entries and cannot list the same cidr spelling twice. Origin rejects a set that would exclude the caller's own address from an enforced list, so keep an entry that admits the address Terraform calls from. Updates send the etag from the last read, so Origin rejects the replace if the entries changed since then. deletion_protection defaults to true, so Terraform will not remove every entry or move them to another namespace until that is set to false and applied.

~> **Note:** This resource owns every entry on the namespace's allowlist. Do not use it together with `cursor_origin_inbound_ip_allowlist_entry` for the same namespace: each apply of this resource removes entries it does not list, and the per-entry resources then plan to add them back. Use one or the other per namespace.

Each create, update, or destroy is one `ReplaceInboundIpAllowlistEntries` call, however many entries change, so large lists stay well under the Origin API's per-user rate limit. CIDRs are stored as spelled: `10.0.0.1/8` and `10.0.0.0/8` are different entries, and changing an entry's spelling removes the old entry and adds a new one with a new ID.

Updates send the `etag` from the last read. If the entries changed since then, Origin rejects the replace with HTTP 409; run `terraform apply -refresh-only` (or `terraform plan`, which refreshes) to review the current entries, then apply again. Creating the resource replaces whatever entries the namespace already lists, so import an existing set instead if you want to review it first.

## Example Usage

```terraform
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
```

## Migrating from cursor_origin_inbound_ip_allowlist_entry

Remove the per-entry resources from state without deleting the entries, then import the namespace into this resource. With Terraform 1.7 or later, use `removed` and `import` blocks and list the same entries in `entry` blocks:

```terraform
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
```

OpenTofu's `removed` block always forgets the resource without destroying it and does not accept a `lifecycle` block, so omit `lifecycle` there.

`terraform plan` should then show only the import. Any difference it shows is an entry that the new configuration adds, changes, or removes.

On older Terraform versions, run `terraform state rm` for the per-entry resources and then import:

```shell
terraform state rm 'cursor_origin_inbound_ip_allowlist_entry.office' 'cursor_origin_inbound_ip_allowlist_entry.egress'
terraform import cursor_origin_inbound_ip_allowlist_entries.acme acme
```

Do not run `terraform destroy` on the per-entry resources or apply with them still in configuration, since that deletes the entries.

<!-- schema generated by tfplugindocs -->
## Schema

### Required

- `namespace` (String) Slug of the team namespace whose allowlist entries this resource owns. Changing this removes every entry from the old namespace and writes the set to the new one. Blocked while deletion_protection is true.

### Optional

- `deletion_protection` (Boolean) When true, Terraform will not destroy this resource, which would remove every entry on the namespace's allowlist. That includes terraform destroy and replacements caused by changing namespace. Set to false and apply before destroying or moving it. Null is treated as protected.
- `entry` (Block Set) One entry on the allowlist. Omit every entry block to remove all entries. At most 1000. (see [below for nested schema](#nestedblock--entry))

### Read-Only

- `etag` (String) Origin's etag for the namespace's entry set as of the last read or write. Sent with updates so a set changed outside Terraform is not overwritten.
- `id` (String) The namespace slug.

<a id="nestedblock--entry"></a>
### Nested Schema for `entry`

Required:

- `cidr` (String) IPv4 or IPv6 address or CIDR range, for example 203.0.113.0/24, 203.0.113.7, or 2001:db8::/32. Stored as spelled, so 10.0.0.1/8 and 10.0.0.0/8 are different entries; each spelling may appear once. A range covering an entire address space (/0) is rejected.

Optional:

- `description` (String) Label for the entry, at most 255 characters. Defaults to an empty string.
- `enabled` (Boolean) Whether the entry admits its addresses. A disabled entry stays listed but admits nothing. Defaults to true.

Read-Only:

- `id` (String) Origin-assigned entry ID (nsip_...). Kept while the same cidr spelling stays listed.

## Import

Import is supported using the following syntax:

The [`terraform import` command](https://developer.hashicorp.com/terraform/cli/commands/import) can be used, for example:

```shell
terraform import cursor_origin_inbound_ip_allowlist_entries.acme acme
```
