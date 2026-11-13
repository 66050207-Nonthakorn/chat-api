variable "aws_region" { type = string }
variable "app_domain" { type = string }
variable "origin_domain" { type = string }
variable "alb_certificate_arn" { type = string }
variable "cloudfront_certificate_arn" { type = string }
variable "existing_user_pool_id" { type = string }
variable "origin_shared_secret" {
  type      = string
  sensitive = true
}
