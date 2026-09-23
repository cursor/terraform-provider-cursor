resource "cursor_origin_ssh_certificate_authority" "production" {
  owner               = "acme"
  name                = "Acme production CA"
  public_key          = file("${path.module}/acme-ssh-ca.pub")
  deletion_protection = true

  # Rotation adds the new key before the old one is removed. Set
  # deletion_protection = false and apply first; a rotation replaces the authority.
  lifecycle {
    create_before_destroy = true
  }
}
