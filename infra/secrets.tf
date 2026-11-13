resource "aws_secretsmanager_secret" "runtime" {
  for_each = toset([
    "database_url",
    "valkey_url",
    "cursor_hmac_key",
    "oidc_client_secret",
    "session_encryption_keys",
  ])

  name                    = replace("${local.name_prefix}/runtime/${each.key}", "_", "-")
  recovery_window_in_days = var.secret_recovery_window_days

  # Secret values are supplied out of band after infrastructure creation.
  # Keeping versions out of Terraform avoids storing credentials in state.
}
