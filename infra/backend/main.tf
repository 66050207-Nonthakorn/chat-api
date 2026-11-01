locals {
  name_prefix = "${var.project}-${var.environment}"

  common_tags = {
    Project     = var.project
    Environment = var.environment
    ManagedBy   = "Terraform"
    Component   = "backend"
  }

  service_settings = {
    api = {
      cpu           = var.api_task_cpu
      memory        = var.api_task_memory_mib
      desired_count = var.api_desired_count
    }
    worker = {
      cpu           = var.worker_task_cpu
      memory        = var.worker_task_memory_mib
      desired_count = var.worker_desired_count
    }
  }
}

resource "aws_ecs_cluster" "backend" {
  name = "${local.name_prefix}-backend"
}

resource "aws_ecr_repository" "service" {
  for_each = local.service_settings

  name                 = "${local.name_prefix}-${each.key}"
  image_tag_mutability = "IMMUTABLE"
  force_delete         = false

  image_scanning_configuration {
    scan_on_push = true
  }

  encryption_configuration {
    encryption_type = "AES256"
  }
}

resource "aws_cloudwatch_log_group" "service" {
  for_each = local.service_settings

  name              = "/ecs/${local.name_prefix}/${each.key}"
  retention_in_days = var.log_retention_days
}

resource "aws_secretsmanager_secret" "runtime" {
  for_each = toset(["database_url", "valkey_url"])

  name                    = replace("${local.name_prefix}/runtime/${each.key}", "_", "-")
  recovery_window_in_days = var.secret_recovery_window_days

  # Secret values are supplied out of band after infrastructure creation.
  # Keeping versions out of Terraform avoids storing credentials in state.
}

resource "aws_security_group" "task" {
  for_each = local.service_settings

  name        = "${local.name_prefix}-${each.key}-tasks"
  description = "Private ${each.key} task network access for ${local.name_prefix}."
  vpc_id      = var.vpc_id

  dynamic "ingress" {
    for_each = each.key == "api" ? [var.shared_alb_security_group_id] : []

    content {
      description     = "API traffic from the externally managed shared ALB."
      protocol        = "tcp"
      from_port       = 8080
      to_port         = 8080
      security_groups = [ingress.value]
    }
  }

  dynamic "egress" {
    for_each = var.task_egress_ipv4_cidrs

    content {
      description = "HTTPS to explicitly configured AWS service or egress destinations."
      protocol    = "tcp"
      from_port   = 443
      to_port     = 443
      cidr_blocks = [egress.value]
    }
  }

  egress {
    description     = "PostgreSQL connections to the private database security group."
    protocol        = "tcp"
    from_port       = 5432
    to_port         = 5432
    security_groups = [aws_security_group.postgres.id]
  }

  egress {
    description     = "TLS connections to the private Valkey security group."
    protocol        = "tcp"
    from_port       = 6379
    to_port         = 6379
    security_groups = [aws_security_group.valkey.id]
  }
}

resource "aws_security_group" "postgres" {
  name        = "${local.name_prefix}-postgres"
  description = "Private PostgreSQL access from backend ECS tasks only."
  vpc_id      = var.vpc_id

  ingress = []
  egress  = []
}

resource "aws_security_group" "valkey" {
  name        = "${local.name_prefix}-valkey"
  description = "Private TLS Valkey access from backend ECS tasks only."
  vpc_id      = var.vpc_id

  ingress = []
  egress  = []
}

resource "aws_vpc_security_group_ingress_rule" "postgres_from_tasks" {
  for_each = local.service_settings

  security_group_id            = aws_security_group.postgres.id
  referenced_security_group_id = aws_security_group.task[each.key].id
  description                  = "PostgreSQL from the ${each.key} task security group."
  ip_protocol                  = "tcp"
  from_port                    = 5432
  to_port                      = 5432
}

resource "aws_vpc_security_group_ingress_rule" "valkey_from_tasks" {
  for_each = local.service_settings

  security_group_id            = aws_security_group.valkey.id
  referenced_security_group_id = aws_security_group.task[each.key].id
  description                  = "Valkey TLS from the ${each.key} task security group."
  ip_protocol                  = "tcp"
  from_port                    = 6379
  to_port                      = 6379
}

resource "aws_db_subnet_group" "postgres" {
  name        = "${local.name_prefix}-postgres"
  description = "Private shared-network subnets for ${local.name_prefix} PostgreSQL."
  subnet_ids  = var.private_subnet_ids
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

resource "aws_elasticache_subnet_group" "valkey" {
  name        = "${local.name_prefix}-valkey"
  description = "Private shared-network subnets for ${local.name_prefix} Valkey."
  subnet_ids  = var.private_subnet_ids
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

resource "aws_ecs_task_definition" "service" {
  for_each = var.enable_ecs_services ? local.service_settings : {}

  family                   = "${local.name_prefix}-${each.key}"
  cpu                      = each.value.cpu
  memory                   = each.value.memory
  network_mode             = "awsvpc"
  requires_compatibilities = ["FARGATE"]
  execution_role_arn       = aws_iam_role.execution[each.key].arn
  task_role_arn            = aws_iam_role.task[each.key].arn

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = var.task_cpu_architecture
  }

  container_definitions = jsonencode([
    {
      name        = each.key
      image       = var.container_image_digests[each.key]
      essential   = true
      stopTimeout = 30
      portMappings = each.key == "api" ? [
        {
          containerPort = 8080
          protocol      = "tcp"
        }
      ] : []
      environment = each.key == "api" ? [
        { name = "APP_ENV", value = "production" },
        { name = "HTTP_ADDR", value = ":8080" }
        ] : [
        { name = "APP_ENV", value = "production" }
      ]
      secrets = [
        {
          name      = "DATABASE_URL"
          valueFrom = aws_secretsmanager_secret.runtime["database_url"].arn
        },
        {
          name      = "VALKEY_URL"
          valueFrom = aws_secretsmanager_secret.runtime["valkey_url"].arn
        }
      ]
      logConfiguration = {
        logDriver = "awslogs"
        options = {
          "awslogs-group"         = aws_cloudwatch_log_group.service[each.key].name
          "awslogs-region"        = var.aws_region
          "awslogs-stream-prefix" = each.key
        }
      }
    }
  ])
}

resource "aws_ecs_service" "service" {
  for_each = var.enable_ecs_services ? local.service_settings : {}

  # Ensure execution permissions are installed before ECS starts pulling images
  # and injecting Secrets Manager values into tasks.
  depends_on = [aws_iam_role_policy.execution]

  name            = "${local.name_prefix}-${each.key}"
  cluster         = aws_ecs_cluster.backend.arn
  task_definition = aws_ecs_task_definition.service[each.key].arn
  launch_type     = "FARGATE"
  desired_count   = each.value.desired_count

  deployment_minimum_healthy_percent = 100
  deployment_maximum_percent         = 200
  health_check_grace_period_seconds  = each.key == "api" ? 120 : null
  wait_for_steady_state              = true

  deployment_circuit_breaker {
    enable   = true
    rollback = true
  }

  network_configuration {
    subnets          = var.private_subnet_ids
    security_groups  = [aws_security_group.task[each.key].id]
    assign_public_ip = false
  }

  dynamic "load_balancer" {
    for_each = each.key == "api" ? [var.api_target_group_arn] : []

    content {
      target_group_arn = load_balancer.value
      container_name   = "api"
      container_port   = 8080
    }
  }

  lifecycle {
    precondition {
      condition     = !var.enable_ecs_services || (contains(keys(var.container_image_digests), each.key) && can(regex("^.+@sha256:[0-9a-f]{64}$", lookup(var.container_image_digests, each.key, ""))))
      error_message = "ECS services require an existing, immutable repository@sha256 image digest for each service."
    }
  }
}
