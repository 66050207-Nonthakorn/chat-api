locals {
  ecs_alarm_dimensions = {
    for service in keys(local.service_settings) : service => {
      ClusterName = aws_ecs_cluster.backend.name
      ServiceName = "${local.name_prefix}-${service}"
    }
  }

  ecs_alarm_definitions = {
    for alarm in flatten([
      for service in keys(local.service_settings) : [
        {
          key       = "${service}-cpu-high"
          service   = service
          metric    = "CPUUtilization"
          threshold = 80
        },
        {
          key       = "${service}-memory-high"
          service   = service
          metric    = "MemoryUtilization"
          threshold = 80
        },
      ]
    ]) : alarm.key => alarm
  }
}

resource "aws_cloudwatch_metric_alarm" "ecs" {
  for_each = local.ecs_alarm_definitions

  alarm_name          = "${local.name_prefix}-${each.value.key}"
  alarm_description   = "Average ECS ${each.value.metric} for ${each.value.service} is above 80 percent."
  namespace           = "AWS/ECS"
  metric_name         = each.value.metric
  statistic           = "Average"
  period              = 300
  evaluation_periods  = 2
  datapoints_to_alarm = 2
  threshold           = each.value.threshold
  comparison_operator = "GreaterThanOrEqualToThreshold"
  treat_missing_data  = "notBreaching"
  dimensions          = local.ecs_alarm_dimensions[each.value.service]
}

resource "aws_cloudwatch_metric_alarm" "postgres_cpu" {
  alarm_name          = "${local.name_prefix}-postgres-cpu-high"
  alarm_description   = "Average RDS CPU utilization is at least 80 percent."
  namespace           = "AWS/RDS"
  metric_name         = "CPUUtilization"
  statistic           = "Average"
  period              = 300
  evaluation_periods  = 2
  datapoints_to_alarm = 2
  threshold           = 80
  comparison_operator = "GreaterThanOrEqualToThreshold"
  treat_missing_data  = "notBreaching"
  dimensions = {
    DBInstanceIdentifier = aws_db_instance.postgres.identifier
  }
}

resource "aws_cloudwatch_metric_alarm" "postgres_storage" {
  alarm_name          = "${local.name_prefix}-postgres-low-storage"
  alarm_description   = "RDS free storage is below 5 GiB."
  namespace           = "AWS/RDS"
  metric_name         = "FreeStorageSpace"
  statistic           = "Minimum"
  period              = 300
  evaluation_periods  = 2
  datapoints_to_alarm = 2
  threshold           = 5368709120
  comparison_operator = "LessThanThreshold"
  treat_missing_data  = "notBreaching"
  dimensions = {
    DBInstanceIdentifier = aws_db_instance.postgres.identifier
  }
}

resource "aws_cloudwatch_metric_alarm" "valkey_cpu" {
  alarm_name          = "${local.name_prefix}-valkey-cpu-high"
  alarm_description   = "Average Valkey node CPU utilization is at least 80 percent."
  namespace           = "AWS/ElastiCache"
  metric_name         = "EngineCPUUtilization"
  statistic           = "Average"
  period              = 300
  evaluation_periods  = 2
  datapoints_to_alarm = 2
  threshold           = 80
  comparison_operator = "GreaterThanOrEqualToThreshold"
  treat_missing_data  = "notBreaching"
  dimensions = {
    ReplicationGroupId = aws_elasticache_replication_group.valkey.id
    Role               = "Primary"
  }
}

resource "aws_cloudwatch_dashboard" "backend" {
  dashboard_name = "${local.name_prefix}-backend"
  dashboard_body = jsonencode({
    widgets = concat(
      [
        for service in keys(local.service_settings) : {
          type   = "metric"
          width  = 12
          height = 6
          properties = {
            title  = "${service} ECS CPU and memory"
            view   = "timeSeries"
            region = var.aws_region
            period = 300
            stat   = "Average"
            metrics = [
              ["AWS/ECS", "CPUUtilization", "ClusterName", aws_ecs_cluster.backend.name, "ServiceName", "${local.name_prefix}-${service}"],
              [".", "MemoryUtilization", ".", ".", ".", "."],
            ]
          }
        }
      ],
      [
        {
          type   = "metric"
          width  = 12
          height = 6
          properties = {
            title  = "PostgreSQL CPU and free storage"
            view   = "timeSeries"
            region = var.aws_region
            period = 300
            stat   = "Average"
            metrics = [
              ["AWS/RDS", "CPUUtilization", "DBInstanceIdentifier", aws_db_instance.postgres.identifier],
              [".", "FreeStorageSpace", ".", ".", { stat = "Minimum", yAxis = "right" }],
            ]
          }
        },
        {
          type   = "metric"
          width  = 12
          height = 6
          properties = {
            title  = "Valkey primary engine CPU"
            view   = "timeSeries"
            region = var.aws_region
            period = 300
            stat   = "Average"
            metrics = [
              ["AWS/ElastiCache", "EngineCPUUtilization", "ReplicationGroupId", aws_elasticache_replication_group.valkey.id, "Role", "Primary"],
            ]
          }
        },
      ]
    )
  })
}
