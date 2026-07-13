resource "aws_lambda_function" "starlogz" {
  function_name = local.name_prefix
  description   = "starlogz MCP server ${var.function_version}"
  runtime       = "provided.al2023"
  handler       = "bootstrap"
  architectures = [var.lambda_arch]
  s3_bucket     = local.deploy_bucket
  s3_key        = var.function_s3_key
  role          = aws_iam_role.lambda.arn
  memory_size   = var.lambda_memory_mb
  timeout       = var.lambda_timeout_sec
  layers        = [var.lambda_web_adapter_layer_arn]

  environment {
    variables = {
      PORT                 = "8088"
      LOG_LEVEL            = "INFO"
      READINESS_CHECK_PATH = "/health"
      EVENT_BUS_NAME       = aws_cloudwatch_event_bus.wide_events.name
      ENVIRONMENT          = var.env
      SERVER_URL           = local.server_url
      GITHUB_CLIENT_ID     = var.github_client_id
      GITHUB_CLIENT_SECRET = var.github_client_secret
      DATABASE_URL         = var.database_url
      TOKEN_ENCRYPTION_KEY = var.token_encryption_key
      JWK_CONTENT          = var.jwk_content
      SENTRY_DSN           = var.sentry_dsn
      SENTRY_ENVIRONMENT   = var.sentry_environment
    }
  }
}

resource "aws_cloudwatch_log_group" "lambda" {
  name              = "/aws/lambda/${aws_lambda_function.starlogz.function_name}"
  retention_in_days = 30
}

resource "aws_lambda_permission" "apigw" {
  statement_id  = "AllowAPIGatewayInvoke"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.starlogz.function_name
  principal     = "apigateway.amazonaws.com"
  source_arn    = "${aws_apigatewayv2_api.starlogz.execution_arn}/*/*"
}
