resource "aws_elasticache_subnet_group" "valkey" {
  name        = "${local.name_prefix}-valkey"
  description = "Private data subnets for ${local.name_prefix} Valkey."
  subnet_ids  = var.data_subnet_ids
}

resource "aws_elasticache_replication_group" "valkey" {
  replication_group_id = "${local.name_prefix}-valkey"
  description          = "Private backend Valkey for ${local.name_prefix}."
  engine               = "valkey"
  engine_version       = var.valkey_engine_version
  node_type            = var.valkey_node_type
  num_cache_clusters   = var.valkey_num_cache_clusters
  port                 = 6379

  auth_token_wo         = var.valkey_auth_token
  auth_token_wo_version = var.valkey_auth_token_version

  subnet_group_name  = aws_elasticache_subnet_group.valkey.name
  security_group_ids = [aws_security_group.valkey.id]

  at_rest_encryption_enabled = true
  transit_encryption_enabled = true
  transit_encryption_mode    = "required"
  automatic_failover_enabled = var.valkey_automatic_failover_enabled
  multi_az_enabled           = var.valkey_automatic_failover_enabled
  snapshot_retention_limit   = 0
  apply_immediately          = false

  lifecycle {
    precondition {
      condition     = !var.valkey_automatic_failover_enabled || var.valkey_num_cache_clusters >= 2
      error_message = "Valkey automatic failover requires at least two cache clusters."
    }
  }
}
