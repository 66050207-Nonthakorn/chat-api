terraform {
  required_version = ">= 1.11, < 2.0"
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
  default_tags {
    tags = { Project = "cloudchat", Environment = "demo" }
  }
}
