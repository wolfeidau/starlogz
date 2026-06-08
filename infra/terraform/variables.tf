variable "aws_region" {
  description = "AWS region to deploy into."
  type        = string
  default     = "ap-southeast-2"
}

variable "env" {
  description = "Environment name (e.g. prod, staging)."
  type        = string
}

variable "branch" {
  description = "Git branch this deployment was made from (e.g. main)."
  type        = string
  default     = "main"
}

variable "component" {
  description = "Component name for tagging (e.g. api)."
  type        = string
  default     = "api"
}

variable "zone_id" {
  description = "Route53 hosted zone ID for the base domain."
  type        = string
}

variable "domain" {
  description = "Base domain name. Service is deployed at starlogz.{domain}."
  type        = string
}

variable "function_s3_key" {
  description = "S3 key of the Lambda zip package (e.g. v0.4.0/function.zip)."
  type        = string
}

variable "function_version" {
  description = "Version string embedded in the Lambda description. Bump to force an update."
  type        = string
}

variable "lambda_memory_mb" {
  description = "Lambda function memory in MB (also governs CPU allocation)."
  type        = number
  default     = 512
}

variable "lambda_timeout_sec" {
  description = "Lambda timeout in seconds. Must be <= API Gateway's 30s maximum."
  type        = number
  default     = 29
}

variable "lambda_arch" {
  description = "Lambda CPU architecture: x86_64 or arm64."
  type        = string
  default     = "x86_64"
}

variable "github_client_id" {
  description = "GitHub App OAuth2 client ID."
  type        = string
}

variable "lambda_web_adapter_layer_arn" {
  description = "ARN of the AWS Lambda Web Adapter layer for the target region and architecture. See https://github.com/awslabs/aws-lambda-web-adapter#lwa-arn"
  type        = string
}

# Sensitive variables — set via terraform.tfvars (gitignored) or environment variables.

variable "github_client_secret" {
  description = "GitHub App OAuth2 client secret."
  type        = string
  sensitive   = true
}

variable "database_url" {
  description = "PostgreSQL connection string. Append ?pool_max_conns=2 to cap per-instance connections."
  type        = string
  sensitive   = true
}

variable "token_encryption_key" {
  description = "Base64-encoded 32-byte key for encrypting stored GitHub tokens (openssl rand -base64 32)."
  type        = string
  sensitive   = true
}

variable "jwk_content" {
  description = "JSON content of the JWK signing key (output of: starlogz-server keygen)."
  type        = string
  sensitive   = true
}

variable "sentry_dsn" {
  description = "Sentry DSN for error reporting. Leave empty to disable."
  type        = string
  sensitive   = true
  default     = ""
}

variable "sentry_environment" {
  description = "Sentry environment tag (e.g. prod, staging)."
  type        = string
  default     = ""
}
