data "cursor_origin_ssh_certificate_authorities" "acme" {
  owner = "acme"
}

output "acme_ssh_ca_fingerprints" {
  value = data.cursor_origin_ssh_certificate_authorities.acme.certificate_authorities[*].fingerprint
}
