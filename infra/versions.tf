terraform {
  required_version = ">= 1.9.0, < 2.0.0"

  # The state bucket is provisioned and secured separately so app cleanup can
  # never delete the state needed to manage or recover this environment.
  backend "s3" {
    encrypt      = true
    use_lockfile = true
  }

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.71.0"
    }
  }
}

provider "aws" {
  region = var.aws_region

  # These switches allow a schema-only plan with dummy credentials. Keep
  # offline_validation false for real AWS operations.
  skip_credentials_validation = var.offline_validation
  skip_requesting_account_id  = var.offline_validation
  skip_metadata_api_check     = var.offline_validation
  skip_region_validation      = var.offline_validation

  default_tags {
    tags = local.common_tags
  }
}
