output "ecs_cluster_name" {
  description = "Backend ECS cluster name."
  value       = aws_ecs_cluster.backend.name
}

output "ecr_repository_urls" {
  description = "Immutable API and worker ECR repository URLs."
  value       = { for service, repository in aws_ecr_repository.service : service => repository.repository_url }
}

output "runtime_secret_arns" {
  description = "Secret shells to populate outside Terraform before enabling ECS services."
  value       = { for name, secret in aws_secretsmanager_secret.runtime : name => secret.arn }
}

output "rds_endpoint" {
  description = "Private PostgreSQL endpoint."
  value       = aws_db_instance.postgres.address
}

output "rds_master_secret_arn" {
  description = "RDS-managed master credential secret; reserved for controlled database administration, not ECS task access."
  value       = try(aws_db_instance.postgres.master_user_secret[0].secret_arn, null)
}

output "valkey_tls_endpoint" {
  description = "Private Valkey TLS endpoint for the rediss:// runtime connection URL."
  value       = aws_elasticache_replication_group.valkey.primary_endpoint_address
}

output "media_bucket_name" {
  description = "Private bucket containing processed chat images."
  value       = aws_s3_bucket.media.id
}

output "task_role_arns" {
  description = "Application task roles, with API and worker media-object permissions."
  value       = { for service, role in aws_iam_role.task : service => role.arn }
}

output "task_execution_role_arns" {
  description = "ECS task execution roles with scoped ECR, log, and runtime-secret permissions."
  value       = { for service, role in aws_iam_role.execution : service => role.arn }
}

output "migration_task_definition_arn" {
  description = "One-off migration task definition; run it and verify exit code before updating ECS services."
  value       = try(aws_ecs_task_definition.migration[0].arn, null)
}
