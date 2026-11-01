data "aws_iam_policy_document" "ecs_task_trust" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["ecs-tasks.amazonaws.com"]
    }

    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [var.aws_account_id]
    }

    condition {
      test     = "ArnLike"
      variable = "aws:SourceArn"
      values   = ["arn:aws:ecs:${var.aws_region}:${var.aws_account_id}:*"]
    }
  }
}

resource "aws_iam_role" "execution" {
  for_each = local.service_settings

  name                 = "${local.name_prefix}-${each.key}-execution"
  assume_role_policy   = data.aws_iam_policy_document.ecs_task_trust.json
  max_session_duration = 3600
}

resource "aws_iam_role" "task" {
  for_each = local.service_settings

  name                 = "${local.name_prefix}-${each.key}-task"
  assume_role_policy   = data.aws_iam_policy_document.ecs_task_trust.json
  max_session_duration = 3600

  # The application task role intentionally has no AWS permissions. Add an
  # action only when application code gains a concrete AWS API requirement.
}

data "aws_iam_policy_document" "execution" {
  for_each = local.service_settings

  statement {
    sid       = "PullOnlyThisServiceImage"
    effect    = "Allow"
    actions   = ["ecr:GetAuthorizationToken"]
    resources = ["*"]
  }

  statement {
    sid    = "PullImagesFromThisRepository"
    effect = "Allow"
    actions = [
      "ecr:BatchCheckLayerAvailability",
      "ecr:BatchGetImage",
      "ecr:GetDownloadUrlForLayer",
    ]
    resources = [aws_ecr_repository.service[each.key].arn]
  }

  statement {
    sid    = "WriteOnlyThisServiceLogGroup"
    effect = "Allow"
    actions = [
      "logs:CreateLogStream",
      "logs:DescribeLogStreams",
      "logs:PutLogEvents",
    ]
    resources = ["${aws_cloudwatch_log_group.service[each.key].arn}:*"]
  }

  statement {
    sid    = "ReadOnlyRuntimeConnectionSecrets"
    effect = "Allow"
    actions = [
      "secretsmanager:DescribeSecret",
      "secretsmanager:GetSecretValue",
    ]
    resources = [
      aws_secretsmanager_secret.runtime["database_url"].arn,
      aws_secretsmanager_secret.runtime["valkey_url"].arn,
    ]
  }
}

resource "aws_iam_role_policy" "execution" {
  for_each = local.service_settings

  name   = "${local.name_prefix}-${each.key}-execution"
  role   = aws_iam_role.execution[each.key].id
  policy = data.aws_iam_policy_document.execution[each.key].json
}
