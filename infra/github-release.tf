variable "github_backend_release_subject" {
  description = "Exact GitHub OIDC subject for the backend repository's main branch. Empty disables image publishing."
  type        = string
  default     = ""

  validation {
    condition     = var.github_backend_release_subject == "" || can(regex("^repo:[A-Za-z0-9_.-]+@[0-9]+/[A-Za-z0-9_.-]+@[0-9]+:ref:refs/heads/main$", var.github_backend_release_subject))
    error_message = "Use an immutable GitHub OIDC subject for the main branch."
  }
}

resource "aws_iam_role" "github_backend_release" {
  count = var.github_backend_release_subject == "" ? 0 : 1
  name  = "${local.name_prefix}-backend-release"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow"
      Action = "sts:AssumeRoleWithWebIdentity"
      Principal = {
        Federated = "arn:aws:iam::${var.aws_account_id}:oidc-provider/token.actions.githubusercontent.com"
      }
      Condition = {
        StringEquals = {
          "token.actions.githubusercontent.com:aud" = "sts.amazonaws.com"
          "token.actions.githubusercontent.com:sub" = var.github_backend_release_subject
        }
      }
    }]
  })
}

resource "aws_iam_role_policy" "github_backend_release" {
  count = length(aws_iam_role.github_backend_release)
  name  = "${local.name_prefix}-backend-release"
  role  = aws_iam_role.github_backend_release[count.index].name
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect   = "Allow"
        Action   = "ecr:GetAuthorizationToken"
        Resource = "*"
      },
      {
        Effect = "Allow"
        Action = [
          "ecr:BatchCheckLayerAvailability",
          "ecr:BatchGetImage",
          "ecr:CompleteLayerUpload",
          "ecr:DescribeImages",
          "ecr:InitiateLayerUpload",
          "ecr:PutImage",
          "ecr:UploadLayerPart",
        ]
        Resource = [for repository in aws_ecr_repository.service : repository.arn]
      },
    ]
  })
}
