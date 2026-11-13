resource "aws_db_subnet_group" "postgres" {
  name        = "${local.name_prefix}-postgres"
  description = "Private data subnets for ${local.name_prefix} PostgreSQL."
  subnet_ids  = var.data_subnet_ids
}

resource "aws_cloudwatch_log_group" "postgresql" {
  name              = "/aws/rds/instance/${local.name_prefix}-postgres/postgresql"
  retention_in_days = var.log_retention_days
}

resource "aws_cloudwatch_log_group" "postgres_upgrade" {
  name              = "/aws/rds/instance/${local.name_prefix}-postgres/upgrade"
  retention_in_days = var.log_retention_days
}

resource "aws_db_instance" "postgres" {
  identifier = "${local.name_prefix}-postgres"

  depends_on = [
    aws_cloudwatch_log_group.postgresql,
    aws_cloudwatch_log_group.postgres_upgrade,
  ]

  engine         = "postgres"
  engine_version = var.postgres_engine_version
  instance_class = var.postgres_instance_class
  db_name        = var.postgres_database_name
  username       = var.postgres_master_username

  manage_master_user_password = true

  allocated_storage       = var.postgres_allocated_storage_gib
  storage_type            = "gp3"
  storage_encrypted       = true
  backup_retention_period = var.postgres_backup_retention_days
  copy_tags_to_snapshot   = true

  db_subnet_group_name   = aws_db_subnet_group.postgres.name
  vpc_security_group_ids = [aws_security_group.postgres.id]
  publicly_accessible    = false
  multi_az               = var.postgres_multi_az

  enabled_cloudwatch_logs_exports = ["postgresql", "upgrade"]
  auto_minor_version_upgrade      = true
  allow_major_version_upgrade     = false
  apply_immediately               = false
  deletion_protection             = var.postgres_deletion_protection
  delete_automated_backups        = true
  skip_final_snapshot             = var.postgres_skip_final_snapshot
  final_snapshot_identifier       = var.postgres_skip_final_snapshot ? null : var.postgres_final_snapshot_identifier

  lifecycle {
    precondition {
      condition     = var.postgres_skip_final_snapshot || var.postgres_final_snapshot_identifier != null
      error_message = "Set a unique postgres_final_snapshot_identifier before creating or destroying this database, or explicitly approve skipping the snapshot."
    }
  }
}
