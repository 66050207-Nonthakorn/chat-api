resource "aws_appautoscaling_target" "service" {
  for_each = var.enable_ecs_services ? local.service_settings : {}

  service_namespace  = "ecs"
  scalable_dimension = "ecs:service:DesiredCount"
  resource_id        = "service/${aws_ecs_cluster.backend.name}/${aws_ecs_service.service[each.key].name}"
  min_capacity       = each.value.desired_count
  # The worker stays fixed; equal bounds also let Terraform change its count.
  max_capacity = each.key == "api" ? var.api_max_count : each.value.desired_count
}

resource "aws_appautoscaling_policy" "api_cpu" {
  count = var.enable_ecs_services ? 1 : 0

  name               = "${local.name_prefix}-api-cpu"
  policy_type        = "TargetTrackingScaling"
  resource_id        = aws_appautoscaling_target.service["api"].resource_id
  scalable_dimension = aws_appautoscaling_target.service["api"].scalable_dimension
  service_namespace  = aws_appautoscaling_target.service["api"].service_namespace

  target_tracking_scaling_policy_configuration {
    target_value       = 60
    scale_out_cooldown = 60
    scale_in_cooldown  = 300

    predefined_metric_specification {
      predefined_metric_type = "ECSServiceAverageCPUUtilization"
    }
  }
}
