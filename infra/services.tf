resource "aws_ecs_task_definition" "service" {
  for_each = var.enable_ecs_services ? local.service_settings : {}

  lifecycle {
    create_before_destroy = true
  }

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
      stopTimeout = var.shutdown_grace_seconds + 5
      portMappings = each.key == "api" ? [
        {
          containerPort = 8080
          protocol      = "tcp"
        }
      ] : []
      environment = concat([
        { name = "APP_ENV", value = "production" },
        { name = "SHUTDOWN_GRACE_SECONDS", value = tostring(var.shutdown_grace_seconds) },
        { name = "AWS_REGION", value = var.aws_region },
        { name = "IMAGE_BUCKET", value = aws_s3_bucket.media.id },
        ], each.key == "api" ? [
        { name = "HTTP_ADDR", value = ":8080" },
        { name = "APP_ORIGIN", value = var.app_origin },
        { name = "OIDC_ISSUER", value = var.oidc_issuer },
        { name = "OIDC_CLIENT_ID", value = var.oidc_client_id },
        { name = "OIDC_REDIRECT_URL", value = var.oidc_redirect_url },
        { name = "SESSION_ENCRYPTION_CURRENT_KEY_ID", value = var.session_encryption_current_key_id },
      ] : [])
      secrets = concat([
        {
          name      = "DATABASE_URL"
          valueFrom = aws_secretsmanager_secret.runtime["database_url"].arn
        },
        {
          name      = "VALKEY_URL"
          valueFrom = aws_secretsmanager_secret.runtime["valkey_url"].arn
        },
        ], each.key == "api" ? [
        {
          name      = "CURSOR_HMAC_KEY"
          valueFrom = aws_secretsmanager_secret.runtime["cursor_hmac_key"].arn
        },
        {
          name      = "OIDC_CLIENT_SECRET"
          valueFrom = aws_secretsmanager_secret.runtime["oidc_client_secret"].arn
        },
        {
          name      = "SESSION_ENCRYPTION_KEYS"
          valueFrom = aws_secretsmanager_secret.runtime["session_encryption_keys"].arn
        },
      ] : [])
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

resource "aws_ecs_task_definition" "migration" {
  count = var.migration_image_digest != "" ? 1 : 0

  family                   = "${local.name_prefix}-migration"
  cpu                      = var.api_task_cpu
  memory                   = var.api_task_memory_mib
  network_mode             = "awsvpc"
  requires_compatibilities = ["FARGATE"]
  execution_role_arn       = aws_iam_role.execution_migration[0].arn
  task_role_arn            = aws_iam_role.task_migration[0].arn

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = var.task_cpu_architecture
  }

  container_definitions = jsonencode([
    {
      name        = "migrate"
      image       = var.migration_image_digest
      command     = ["/app/migrate"]
      essential   = true
      stopTimeout = var.shutdown_grace_seconds + 5
      environment = [
        { name = "APP_ENV", value = "production" },
      ]
      secrets = [
        {
          name      = "DATABASE_URL"
          valueFrom = aws_secretsmanager_secret.runtime["database_url"].arn
        },
      ]
      logConfiguration = {
        logDriver = "awslogs"
        options = {
          "awslogs-group"         = aws_cloudwatch_log_group.service["api"].name
          "awslogs-region"        = var.aws_region
          "awslogs-stream-prefix" = "migration"
        }
      }
    }
  ])

  depends_on = [aws_iam_role_policy.execution_migration]
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
    # Application Auto Scaling owns desired counts after service creation.
    ignore_changes = [desired_count]

    precondition {
      condition     = !var.enable_ecs_services || (contains(keys(var.container_image_digests), each.key) && can(regex("^.+@sha256:[0-9a-f]{64}$", lookup(var.container_image_digests, each.key, ""))))
      error_message = "ECS services require an existing, immutable repository@sha256 image digest for each service."
    }

    precondition {
      condition = !var.enable_ecs_services || (
        can(regex("^https://[^/]+$", var.app_origin)) &&
        can(regex("^https://[^/]+/.+$", var.oidc_issuer)) &&
        trimspace(var.oidc_client_id) != "" &&
        var.oidc_redirect_url == "${var.app_origin}/auth/callback" &&
        trimspace(var.session_encryption_current_key_id) != ""
      )
      error_message = "ECS services require valid HTTPS APP_ORIGIN/OIDC_ISSUER, OIDC_CLIENT_ID, matching OIDC_REDIRECT_URL, and SESSION_ENCRYPTION_CURRENT_KEY_ID values supplied by the external identity/session owner."
    }
  }
}
