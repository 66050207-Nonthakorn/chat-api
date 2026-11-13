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

  egress = []
}

resource "aws_security_group" "valkey" {
  name        = "${local.name_prefix}-valkey"
  description = "Private TLS Valkey access from backend ECS tasks only."
  vpc_id      = var.vpc_id

  egress = []
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
