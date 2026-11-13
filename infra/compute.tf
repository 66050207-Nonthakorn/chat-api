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
