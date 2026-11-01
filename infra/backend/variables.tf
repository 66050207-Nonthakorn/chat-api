variable "aws_region" {
  description = "AWS region; confirm service availability, cost, and account eligibility before apply."
  type        = string
}

variable "aws_account_id" {
  description = "12-digit AWS account ID used to scope ECS task-role trust policies."
  type        = string

  validation {
    condition     = can(regex("^[0-9]{12}$", var.aws_account_id))
    error_message = "aws_account_id must contain exactly 12 digits."
  }
}

variable "project" {
  description = "Short lowercase project name used in resource names."
  type        = string
  default     = "cloudchat"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{1,15}$", var.project))
    error_message = "project must be 2-16 lowercase letters, digits, or hyphens and start with a letter."
  }
}

variable "environment" {
  description = "Environment name used to isolate resource names."
  type        = string
  default     = "dev"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{1,9}$", var.environment))
    error_message = "environment must be 2-10 lowercase letters, digits, or hyphens and start with a letter."
  }
}

variable "vpc_id" {
  description = "VPC ID from the shared networking stack; this module does not create or modify the VPC."
  type        = string
}

variable "private_subnet_ids" {
  description = "At least two private subnet IDs in different AZs from the shared networking stack."
  type        = list(string)

  validation {
    condition     = length(var.private_subnet_ids) >= 2 && length(distinct(var.private_subnet_ids)) == length(var.private_subnet_ids)
    error_message = "private_subnet_ids must contain at least two distinct subnet IDs."
  }
}

variable "shared_alb_security_group_id" {
  description = "Security group owned by the shared ingress stack; it may reach only the API task port."
  type        = string
}

variable "api_target_group_arn" {
  description = "Existing shared ALB target group ARN. The target group must use target_type=ip and health check /health/ready."
  type        = string
  default     = ""

  validation {
    condition     = !var.enable_ecs_services || can(regex("^arn:[^:]+:elasticloadbalancing:[^:]+:[0-9]{12}:targetgroup/.+", var.api_target_group_arn))
    error_message = "Set api_target_group_arn to the shared ALB target-group ARN when ECS services are enabled."
  }
}

variable "task_egress_ipv4_cidrs" {
  description = "Explicit IPv4 egress destinations for HTTPS access to ECR, Secrets Manager, CloudWatch Logs, and approved AWS endpoints."
  type        = list(string)
  default     = ["0.0.0.0/0"]

  validation {
    condition     = length(var.task_egress_ipv4_cidrs) > 0 && alltrue([for cidr in var.task_egress_ipv4_cidrs : can(cidrhost(cidr, 0))])
    error_message = "task_egress_ipv4_cidrs must contain one or more valid IPv4 CIDR ranges."
  }
}

variable "container_image_digests" {
  description = "Immutable full image references keyed by api and worker, for example repository@sha256:<64 hex digits>."
  type        = map(string)

  validation {
    condition = alltrue([
      for service in ["api", "worker"] : contains(keys(var.container_image_digests), service) && can(regex("^.+@sha256:[0-9a-f]{64}$", lookup(var.container_image_digests, service, "")))
    ])
    error_message = "container_image_digests must include api and worker image references pinned by a 64-character sha256 digest."
  }
}

variable "migration_image_digest" {
  description = "Optional API image digest used by the controlled one-off migration task. Set and apply it separately before changing service image digests."
  type        = string
  default     = ""

  validation {
    condition     = var.migration_image_digest == "" || can(regex("^.+@sha256:[0-9a-f]{64}$", var.migration_image_digest))
    error_message = "migration_image_digest must be empty or a full repository@sha256 image reference."
  }
}

variable "enable_ecs_services" {
  description = "Create ECS task definitions and services after immutable images exist and runtime secrets have values."
  type        = bool
  default     = false
}

variable "app_origin" {
  description = "Externally owned browser origin used for OIDC redirects and CSRF checks. Required when ECS services are enabled."
  type        = string
  default     = ""
}

variable "oidc_issuer" {
  description = "Externally owned OIDC issuer URL. Cognito resources are not managed by this backend module."
  type        = string
  default     = ""
}

variable "oidc_client_id" {
  description = "OIDC app-client ID supplied by the identity owner."
  type        = string
  default     = ""
}

variable "oidc_redirect_url" {
  description = "Exact externally registered callback URL; must be APP_ORIGIN/auth/callback."
  type        = string
  default     = ""
}

variable "session_encryption_current_key_id" {
  description = "Key ID selected from the out-of-band SESSION_ENCRYPTION_KEYS secret."
  type        = string
  default     = ""
}

variable "shutdown_grace_seconds" {
  description = "Application drain budget in seconds. ECS stopTimeout adds five seconds and stays within its 30-second configured bound."
  type        = number
  default     = 20

  validation {
    condition     = var.shutdown_grace_seconds >= 1 && var.shutdown_grace_seconds <= 25 && floor(var.shutdown_grace_seconds) == var.shutdown_grace_seconds
    error_message = "shutdown_grace_seconds must be a whole number from 1 through 25."
  }
}

variable "offline_validation" {
  description = "Skip AWS provider identity and region checks for schema-only fmt/validate/plan with dummy credentials. Never enable for apply."
  type        = bool
  default     = false
}

variable "task_cpu_architecture" {
  description = "CPU architecture of the pushed container images."
  type        = string
  default     = "X86_64"

  validation {
    condition     = contains(["X86_64", "ARM64"], var.task_cpu_architecture)
    error_message = "task_cpu_architecture must be X86_64 or ARM64 and must match both pushed images."
  }
}

variable "api_task_cpu" {
  description = "Fargate CPU units for each API task; 512 is the unapproved cost-estimate candidate."
  type        = number
  default     = 512
}

variable "api_task_memory_mib" {
  description = "Fargate memory in MiB for each API task; 1024 is the unapproved cost-estimate candidate."
  type        = number
  default     = 1024
}

variable "api_desired_count" {
  description = "Fixed API task count; autoscaling is intentionally not included."
  type        = number
  default     = 2

  validation {
    condition     = var.api_desired_count >= 1 && floor(var.api_desired_count) == var.api_desired_count
    error_message = "api_desired_count must be a positive whole number."
  }
}

variable "worker_task_cpu" {
  description = "Fargate CPU units for each worker task; 256 is the unapproved cost-estimate candidate."
  type        = number
  default     = 256
}

variable "worker_task_memory_mib" {
  description = "Fargate memory in MiB for each worker task; 512 is the unapproved cost-estimate candidate."
  type        = number
  default     = 512
}

variable "worker_desired_count" {
  description = "Fixed worker task count; autoscaling is intentionally not included."
  type        = number
  default     = 1

  validation {
    condition     = var.worker_desired_count >= 1 && floor(var.worker_desired_count) == var.worker_desired_count
    error_message = "worker_desired_count must be a positive whole number."
  }
}

variable "postgres_engine_version" {
  description = "Pinned RDS PostgreSQL engine version; confirm support in the selected region before apply."
  type        = string
  default     = "17.6"
}

variable "postgres_instance_class" {
  description = "RDS DB instance class; db.t4g.small is an unapproved development estimate."
  type        = string
  default     = "db.t4g.small"
}

variable "postgres_allocated_storage_gib" {
  description = "Initial encrypted gp3 storage in GiB; 20 is an unapproved development estimate."
  type        = number
  default     = 20

  validation {
    condition     = var.postgres_allocated_storage_gib >= 20 && floor(var.postgres_allocated_storage_gib) == var.postgres_allocated_storage_gib
    error_message = "postgres_allocated_storage_gib must be a whole number of at least 20."
  }
}

variable "postgres_backup_retention_days" {
  description = "Automated RDS backup retention in days."
  type        = number
  default     = 7

  validation {
    condition     = var.postgres_backup_retention_days >= 1 && var.postgres_backup_retention_days <= 35 && floor(var.postgres_backup_retention_days) == var.postgres_backup_retention_days
    error_message = "postgres_backup_retention_days must be a whole number from 1 through 35."
  }
}

variable "postgres_multi_az" {
  description = "Enable an RDS standby; false matches the unapproved development cost estimate."
  type        = bool
  default     = false
}

variable "postgres_deletion_protection" {
  description = "Prevent accidental deletion of the RDS instance. Disable explicitly only during approved cleanup."
  type        = bool
  default     = true
}

variable "postgres_skip_final_snapshot" {
  description = "Skip the final RDS snapshot only for an explicitly approved disposable environment."
  type        = bool
  default     = false
}

variable "postgres_final_snapshot_identifier" {
  description = "Unique final snapshot name required when postgres_skip_final_snapshot is false."
  type        = string
  default     = null

  validation {
    condition     = var.postgres_final_snapshot_identifier == null || can(regex("^[a-zA-Z][a-zA-Z0-9-]{0,62}$", var.postgres_final_snapshot_identifier))
    error_message = "postgres_final_snapshot_identifier must start with a letter and contain only letters, digits, and hyphens."
  }
}

variable "postgres_master_username" {
  description = "RDS master username; RDS generates and manages its password in Secrets Manager."
  type        = string
  default     = "chat_master"
}

variable "postgres_database_name" {
  description = "Initial PostgreSQL database name."
  type        = string
  default     = "chat"
}

variable "valkey_engine_version" {
  description = "Pinned Valkey engine version; confirm support in the selected region before apply."
  type        = string
  default     = "8.0"
}

variable "valkey_node_type" {
  description = "Node-based ElastiCache Valkey node class; cache.t4g.micro is an unapproved development estimate."
  type        = string
  default     = "cache.t4g.micro"
}

variable "valkey_num_cache_clusters" {
  description = "Number of nodes in the Valkey replication group; one node has no automatic failover."
  type        = number
  default     = 1

  validation {
    condition     = var.valkey_num_cache_clusters >= 1 && floor(var.valkey_num_cache_clusters) == var.valkey_num_cache_clusters
    error_message = "valkey_num_cache_clusters must be a positive whole number."
  }
}

variable "valkey_automatic_failover_enabled" {
  description = "Enable automatic failover for Valkey when at least two cache nodes are configured."
  type        = bool
  default     = false
}

variable "valkey_auth_token" {
  description = "Valkey AUTH token. Supply through a protected TF_VAR_valkey_auth_token source; the provider uses a write-only argument."
  type        = string
  sensitive   = true

  validation {
    condition     = can(regex("^[A-Za-z0-9!&#$^<>-]{16,128}$", var.valkey_auth_token))
    error_message = "valkey_auth_token must be 16-128 characters using only letters, digits, and !, &, #, $, ^, <, >, or -."
  }
}

variable "valkey_auth_token_version" {
  description = "Increment to send a rotated write-only Valkey AUTH token to ElastiCache."
  type        = number
  default     = 1

  validation {
    condition     = var.valkey_auth_token_version >= 1 && floor(var.valkey_auth_token_version) == var.valkey_auth_token_version
    error_message = "valkey_auth_token_version must be a positive whole number."
  }
}

variable "log_retention_days" {
  description = "CloudWatch log retention. Short retention limits storage charges."
  type        = number
  default     = 7

  validation {
    condition     = contains([1, 3, 5, 7, 14, 30, 60, 90, 120, 150, 180, 365, 400, 545, 731, 1827, 3653], var.log_retention_days)
    error_message = "log_retention_days must be a supported CloudWatch Logs retention value."
  }
}

variable "secret_recovery_window_days" {
  description = "Secrets Manager recovery window after deletion."
  type        = number
  default     = 7

  validation {
    condition     = var.secret_recovery_window_days >= 7 && var.secret_recovery_window_days <= 30 && floor(var.secret_recovery_window_days) == var.secret_recovery_window_days
    error_message = "secret_recovery_window_days must be a whole number from 7 through 30."
  }
}
