locals {
  apigw_routes = toset([
    "GET /",
    "GET /dashboard",
    "GET /health",
    "GET /login",
    "GET /logout",
    "GET /public/{proxy+}",
    "GET /.well-known/oauth-authorization-server",
    "GET /.well-known/openid-configuration",
    "GET /.well-known/jwks",
    "GET /.well-known/oauth-protected-resource",
    "POST /oauth2/register",
    "GET /oauth2/authorize",
    "POST /oauth2/authorize/confirm",
    "POST /oauth2/token",
    "GET /auth/github/callback",
    "GET /ui/auth/callback",
    "POST /auth/logout",
    "GET /starlogz.v1.UIService/{method}",
    "POST /starlogz.v1.UIService/{method}",
    "GET /mcp",
    "POST /mcp",
    "DELETE /mcp",
  ])
}

resource "aws_apigatewayv2_api" "starlogz" {
  name          = local.name_prefix
  protocol_type = "HTTP"

  cors_configuration {
    allow_origins = ["*"]
    allow_methods = ["GET", "POST", "DELETE", "OPTIONS"]
    allow_headers = ["content-type", "authorization"]
  }
}

resource "aws_apigatewayv2_integration" "lambda" {
  api_id                 = aws_apigatewayv2_api.starlogz.id
  integration_type       = "AWS_PROXY"
  integration_uri        = aws_lambda_function.starlogz.invoke_arn
  payload_format_version = "2.0"
}

resource "aws_cloudwatch_log_group" "apigw" {
  name              = "/aws/apigateway/${local.name_prefix}"
  retention_in_days = 30
}

resource "aws_apigatewayv2_route" "routes" {
  for_each  = local.apigw_routes
  api_id    = aws_apigatewayv2_api.starlogz.id
  route_key = each.value
  target    = "integrations/${aws_apigatewayv2_integration.lambda.id}"
}

resource "aws_apigatewayv2_stage" "default" {
  api_id      = aws_apigatewayv2_api.starlogz.id
  name        = "$default"
  auto_deploy = true

  depends_on = [aws_apigatewayv2_route.routes]

  access_log_settings {
    destination_arn = aws_cloudwatch_log_group.apigw.arn
    format = jsonencode({
      request_id             = "$context.requestId"
      route_key              = "$context.routeKey"
      http_method            = "$context.httpMethod"
      status                 = "$context.status"
      response_length        = "$context.responseLength"
      response_latency_ms    = "$context.responseLatency"
      integration_latency_ms = "$context.integrationLatency"
      integration_status     = "$context.integrationStatus"
      domain_name            = "$context.domainName"
      protocol               = "$context.protocol"
    })
  }

  default_route_settings {
    throttling_burst_limit = 100
    throttling_rate_limit  = 50
  }

  # Tighter limit on the token endpoint — primary target for credential abuse.
  route_settings {
    route_key              = "POST /oauth2/token"
    throttling_burst_limit = 20
    throttling_rate_limit  = 5
  }

  # Open DCR persists rows, so constrain anonymous registration bursts.
  route_settings {
    route_key              = "POST /oauth2/register"
    throttling_burst_limit = 10
    throttling_rate_limit  = 1
  }
}

resource "aws_apigatewayv2_domain_name" "starlogz" {
  domain_name = local.service_hostname

  domain_name_configuration {
    certificate_arn = aws_acm_certificate_validation.starlogz.certificate_arn
    endpoint_type   = "REGIONAL"
    security_policy = "TLS_1_2"
  }
}

resource "aws_apigatewayv2_api_mapping" "starlogz" {
  api_id      = aws_apigatewayv2_api.starlogz.id
  domain_name = aws_apigatewayv2_domain_name.starlogz.id
  stage       = aws_apigatewayv2_stage.default.id
}
