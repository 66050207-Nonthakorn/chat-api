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
