output "service_url" {
  description = "Service base URL. Use this as SERVER_URL."
  value       = local.server_url
}

output "github_oauth_callback_url" {
  description = "GitHub App callback URL for MCP/OAuth clients."
  value       = "${local.server_url}/auth/github/callback"
}

output "ui_oauth_callback_url" {
  description = "First-party UI OAuth redirect URI."
  value       = "${local.server_url}/ui/auth/callback"
}

output "function_name" {
  description = "Lambda function name. Use with aws lambda update-function-code for code-only deploys."
  value       = aws_lambda_function.starlogz.function_name
}

output "apigw_invoke_url" {
  description = "Raw API Gateway invoke URL (before custom domain). Useful for debugging."
  value       = aws_apigatewayv2_stage.default.invoke_url
}

output "operations_sns_topic_arn" {
  description = "SNS topic receiving operational alarm state transitions."
  value       = aws_sns_topic.operations.arn
}
