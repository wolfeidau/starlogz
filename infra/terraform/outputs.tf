output "service_url" {
  description = "Service base URL. Use this as SERVER_URL and as the base for the GitHub App callback URL ({service_url}/auth/github/callback)."
  value       = local.server_url
}

output "function_name" {
  description = "Lambda function name. Use with aws lambda update-function-code for code-only deploys."
  value       = aws_lambda_function.starlogz.function_name
}

output "apigw_invoke_url" {
  description = "Raw API Gateway invoke URL (before custom domain). Useful for debugging."
  value       = aws_apigatewayv2_stage.default.invoke_url
}
