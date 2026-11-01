locals {
  application_metric_namespace = "CloudChat/${local.name_prefix}"

  application_metric_filters = {
    api_http_5xx = {
      service = "api"
      metric  = "HTTP5xxCount"
      pattern = "{ $.event = \"http.request\" && $.status >= 500 }"
      value   = "1"
      unit    = "Count"
    }
    api_http_latency = {
      service = "api"
      metric  = "HTTPRequestLatencyMs"
      pattern = "{ $.event = \"http.request\" && $.duration_ms = * }"
      value   = "$.duration_ms"
      unit    = "Milliseconds"
    }
    api_room_sync_latency = {
      service = "api"
      metric  = "RoomSyncLatencyMs"
      pattern = "{ $.event = \"http.request\" && $.route = \"api.room.sync\" && $.duration_ms = * }"
      value   = "$.duration_ms"
      unit    = "Milliseconds"
    }
    api_room_sync_count = {
      service = "api"
      metric  = "RoomSyncRequestCount"
      pattern = "{ $.event = \"http.request\" && $.route = \"api.room.sync\" }"
      value   = "1"
      unit    = "Count"
    }
    api_room_sync_failures = {
      service = "api"
      metric  = "RoomSyncFailureCount"
      pattern = "{ $.event = \"http.request\" && $.route = \"api.room.sync\" && $.status >= 500 }"
      value   = "1"
      unit    = "Count"
    }
    api_socket_active = {
      service = "api"
      metric  = "SocketActiveConnections"
      pattern = "{ $.event = \"socket.health\" && $.active_sockets = * }"
      value   = "$.active_sockets"
      unit    = "Count"
    }
    api_socket_ack_latency = {
      service = "api"
      metric  = "SocketAcknowledgementQueueLatencyMs"
      pattern = "{ $.event = \"socket.ack\" && $.state = \"queued\" && $.enqueue_duration_ms = * }"
      value   = "$.enqueue_duration_ms"
      unit    = "Milliseconds"
    }
    api_socket_ack_rejected = {
      service = "api"
      metric  = "SocketAcknowledgementErrorCount"
      pattern = "{ $.event = \"socket.ack\" && $.state = \"error\" }"
      value   = "1"
      unit    = "Count"
    }
    api_socket_ack_write_failures = {
      service = "api"
      metric  = "SocketAcknowledgementErrorCount"
      pattern = "{ $.event = \"socket.ack\" && $.state = \"failed\" }"
      value   = "1"
      unit    = "Count"
    }
    api_socket_backpressure = {
      service = "api"
      metric  = "SocketBackpressureCloseCount"
      pattern = "{ $.event = \"socket.connection\" && $.close_code = 1013 }"
      value   = "1"
      unit    = "Count"
    }
    api_broker_failures = {
      service = "api"
      metric  = "BrokerFailureCount"
      pattern = "{ $.event = \"dependency.health\" && $.dependency = \"valkey_pubsub\" && $.state = \"unavailable\" }"
      value   = "1"
      unit    = "Count"
    }
    worker_broker_publish_failures = {
      service = "worker"
      metric  = "BrokerFailureCount"
      pattern = "{ $.event = \"outbox.retry\" && $.error_code = \"VALKEY_PUBLISH_FAILED\" }"
      value   = "1"
      unit    = "Count"
    }
    api_database_pool = {
      service = "api"
      metric  = "DatabasePoolInUsePercent"
      pattern = "{ $.event = \"database.pool\" && $.pool_in_use_percent = * }"
      value   = "$.pool_in_use_percent"
      unit    = "Percent"
    }
    api_dependency_failures = {
      service = "api"
      metric  = "DependencyFailureCount"
      pattern = "{ $.event = \"dependency.health\" && $.state = \"unavailable\" }"
      value   = "1"
      unit    = "Count"
    }
    worker_dependency_failures = {
      service = "worker"
      metric  = "DependencyFailureCount"
      pattern = "{ $.event = \"dependency.health\" && $.state = \"unavailable\" }"
      value   = "1"
      unit    = "Count"
    }
    worker_outbox_age = {
      service = "worker"
      metric  = "OutboxOldestAgeSeconds"
      pattern = "{ $.event = \"outbox.health\" && $.oldest_age_seconds = * }"
      value   = "$.oldest_age_seconds"
      unit    = "Seconds"
    }
    worker_outbox_pending = {
      service = "worker"
      metric  = "OutboxPendingCount"
      pattern = "{ $.event = \"outbox.health\" && $.pending_count = * }"
      value   = "$.pending_count"
      unit    = "Count"
    }
    worker_outbox_retries = {
      service = "worker"
      metric  = "OutboxRetryCount"
      pattern = "{ $.event = \"outbox.retry\" }"
      value   = "1"
      unit    = "Count"
    }
  }

  application_alarm_definitions = {
    api_http_5xx = {
      metric    = "HTTP5xxCount"
      statistic = "Sum"
      threshold = 10
      operator  = "GreaterThanOrEqualToThreshold"
      title     = "API HTTP 5xx responses"
    }
    api_http_latency = {
      metric    = "HTTPRequestLatencyMs"
      statistic = "Average"
      threshold = 1000
      operator  = "GreaterThanOrEqualToThreshold"
      title     = "API average request latency exceeds 1 second"
    }
    room_sync_failures = {
      metric    = "RoomSyncFailureCount"
      statistic = "Sum"
      threshold = 1
      operator  = "GreaterThanOrEqualToThreshold"
      title     = "Room sync requests fail"
    }
    api_socket_backpressure = {
      metric    = "SocketBackpressureCloseCount"
      statistic = "Sum"
      threshold = 3
      operator  = "GreaterThanOrEqualToThreshold"
      title     = "WebSocket backpressure closes"
    }
    socket_ack_errors = {
      metric    = "SocketAcknowledgementErrorCount"
      statistic = "Sum"
      threshold = 3
      operator  = "GreaterThanOrEqualToThreshold"
      title     = "WebSocket acknowledgement errors"
    }
    dependency_failures = {
      metric    = "DependencyFailureCount"
      statistic = "Sum"
      threshold = 1
      operator  = "GreaterThanOrEqualToThreshold"
      title     = "Backend dependency failures"
    }
    database_pool_pressure = {
      metric    = "DatabasePoolInUsePercent"
      statistic = "Average"
      threshold = 80
      operator  = "GreaterThanOrEqualToThreshold"
      title     = "Database connection pool pressure"
    }
    outbox_age = {
      metric    = "OutboxOldestAgeSeconds"
      statistic = "Maximum"
      threshold = 30
      operator  = "GreaterThanOrEqualToThreshold"
      title     = "Oldest unpublished outbox event"
    }
    outbox_retries = {
      metric    = "OutboxRetryCount"
      statistic = "Sum"
      threshold = 5
      operator  = "GreaterThanOrEqualToThreshold"
      title     = "Repeated outbox retries"
    }
  }

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

resource "aws_cloudwatch_log_metric_filter" "application" {
  for_each = local.application_metric_filters

  name           = "${local.name_prefix}-${replace(each.key, "_", "-")}-metric"
  log_group_name = aws_cloudwatch_log_group.service[each.value.service].name
  pattern        = each.value.pattern

  metric_transformation {
    name      = each.value.metric
    namespace = local.application_metric_namespace
    value     = each.value.value
    unit      = each.value.unit
  }
}

resource "aws_cloudwatch_metric_alarm" "application" {
  for_each = local.application_alarm_definitions

  alarm_name          = "${local.name_prefix}-${replace(each.key, "_", "-")}-alarm"
  alarm_description   = each.value.title
  namespace           = local.application_metric_namespace
  metric_name         = each.value.metric
  statistic           = each.value.statistic
  period              = 60
  evaluation_periods  = 2
  datapoints_to_alarm = 2
  threshold           = each.value.threshold
  comparison_operator = each.value.operator
  treat_missing_data  = "notBreaching"
}

resource "aws_cloudwatch_metric_alarm" "broker_failure" {
  alarm_name          = "${local.name_prefix}-realtime-broker-failure-alarm"
  alarm_description   = "The API realtime Valkey Pub/Sub connection reported unavailable."
  namespace           = local.application_metric_namespace
  metric_name         = "BrokerFailureCount"
  statistic           = "Sum"
  period              = 60
  evaluation_periods  = 1
  datapoints_to_alarm = 1
  threshold           = 1
  comparison_operator = "GreaterThanOrEqualToThreshold"
  treat_missing_data  = "notBreaching"
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
      ],
      [
        {
          type   = "metric"
          width  = 12
          height = 6
          properties = {
            title  = "API request errors and latency"
            view   = "timeSeries"
            region = var.aws_region
            period = 60
            metrics = [
              [local.application_metric_namespace, "HTTP5xxCount", { stat = "Sum", yAxis = "left" }],
              [local.application_metric_namespace, "HTTPRequestLatencyMs", { stat = "Average", yAxis = "right" }],
            ]
          }
        },
        {
          type   = "metric"
          width  = 12
          height = 6
          properties = {
            title  = "Realtime socket health"
            view   = "timeSeries"
            region = var.aws_region
            period = 60
            metrics = [
              [local.application_metric_namespace, "SocketActiveConnections", { stat = "Maximum", yAxis = "left" }],
              [local.application_metric_namespace, "SocketBackpressureCloseCount", { stat = "Sum", yAxis = "right" }],
            ]
          }
        },
        {
          type   = "metric"
          width  = 12
          height = 6
          properties = {
            title  = "Socket acknowledgements, room sync, and broker"
            view   = "timeSeries"
            region = var.aws_region
            period = 60
            metrics = [
              [local.application_metric_namespace, "SocketAcknowledgementQueueLatencyMs", { stat = "Average", yAxis = "left" }],
              [local.application_metric_namespace, "SocketAcknowledgementErrorCount", { stat = "Sum", yAxis = "right" }],
              [local.application_metric_namespace, "RoomSyncRequestCount", { stat = "Sum", yAxis = "left" }],
              [local.application_metric_namespace, "RoomSyncLatencyMs", { stat = "Average", yAxis = "left" }],
              [local.application_metric_namespace, "RoomSyncFailureCount", { stat = "Sum", yAxis = "right" }],
              [local.application_metric_namespace, "BrokerFailureCount", { stat = "Sum", yAxis = "right" }],
            ]
          }
        },
        {
          type   = "metric"
          width  = 12
          height = 6
          properties = {
            title  = "Outbox recovery, dependencies, and database pool"
            view   = "timeSeries"
            region = var.aws_region
            period = 60
            metrics = [
              [local.application_metric_namespace, "OutboxOldestAgeSeconds", { stat = "Maximum", yAxis = "left" }],
              [local.application_metric_namespace, "OutboxPendingCount", { stat = "Maximum", yAxis = "left" }],
              [local.application_metric_namespace, "OutboxRetryCount", { stat = "Sum", yAxis = "right" }],
              [local.application_metric_namespace, "DependencyFailureCount", { stat = "Sum", yAxis = "right" }],
              [local.application_metric_namespace, "DatabasePoolInUsePercent", { stat = "Average", yAxis = "right" }],
            ]
          }
        },
      ]
    )
  })
}
