resource "aws_iam_role" "presignup" {
  name = "cloudchat-demo-presignup"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Action    = "sts:AssumeRole"
      Principal = { Service = "lambda.amazonaws.com" }
    }]
  })
}

resource "aws_cloudwatch_log_group" "presignup" {
  name              = "/aws/lambda/cloudchat-demo-presignup"
  retention_in_days = 7
}

resource "aws_iam_role_policy" "presignup_logs" {
  name = "cloudchat-demo-presignup-logs"
  role = aws_iam_role.presignup.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = ["logs:CreateLogStream", "logs:PutLogEvents"]
      Resource = "${aws_cloudwatch_log_group.presignup.arn}:*"
    }]
  })
}

resource "aws_lambda_function" "presignup" {
  function_name    = "cloudchat-demo-presignup"
  role             = aws_iam_role.presignup.arn
  handler          = "pre_signup.handler"
  runtime          = "python3.12"
  filename         = "${path.module}/pre_signup.zip"
  source_code_hash = filebase64sha256("${path.module}/pre_signup.zip")
  timeout          = 5
  depends_on       = [aws_iam_role_policy.presignup_logs]
}

resource "aws_cognito_user_pool" "chat" {
  name                     = "cloudchat-demo"
  auto_verified_attributes = ["email"]
  username_attributes      = ["email"]
  admin_create_user_config { allow_admin_create_user_only = false }
}

resource "aws_lambda_permission" "cognito_presignup" {
  statement_id  = "AllowCognitoPreSignup"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.presignup.function_name
  principal     = "cognito-idp.amazonaws.com"
  source_arn    = "arn:aws:cognito-idp:${var.aws_region}:${data.aws_caller_identity.current.account_id}:userpool/${var.existing_user_pool_id}"
}
resource "aws_cognito_user_pool_domain" "chat" {
  domain       = "cloudchat-demo-${data.aws_caller_identity.current.account_id}"
  user_pool_id = aws_cognito_user_pool.chat.id
}
resource "aws_cognito_user_pool_client" "chat" {
  name                                 = "cloudchat-demo"
  user_pool_id                         = aws_cognito_user_pool.chat.id
  generate_secret                      = true
  explicit_auth_flows                  = ["ALLOW_USER_PASSWORD_AUTH", "ALLOW_USER_SRP_AUTH", "ALLOW_REFRESH_TOKEN_AUTH", "ALLOW_CUSTOM_AUTH"]
  allowed_oauth_flows_user_pool_client = true
  allowed_oauth_flows                  = ["code"]
  allowed_oauth_scopes                 = ["openid", "email", "profile"]
  callback_urls                        = ["https://${var.app_domain}/auth/callback"]
  logout_urls                          = ["https://${var.app_domain}/"]
  supported_identity_providers         = ["COGNITO"]
}
