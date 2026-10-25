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
  description = "Base domain name. Prod is deployed at starlogz.{domain}; other envs use starlogz-{env}.{domain}."
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

variable "cimd_enabled" {
  description = "Enable OAuth Client ID Metadata Document support. Set false to disable quickly if interoperability or security issues appear."
  type        = bool
  default     = true
}

variable "github_client_id" {
  description = "GitHub App OAuth2 client ID."
  type        = string
}

variable "operator_github_ids" {
  description = "GitHub numeric IDs allowed to access service-wide operations."
  type        = list(number)
  default     = []

  validation {
    condition     = alltrue([for id in var.operator_github_ids : id > 0 && floor(id) == id])
    error_message = "operator_github_ids must contain positive integer GitHub IDs."
  }
}

variable "lambda_web_adapter_layer_arn" {
  description = "ARN of the AWS Lambda Web Adapter layer for the target region and architecture. See https://github.com/awslabs/aws-lambda-web-adapter#lwa-arn"
  type        = string
}

variable "alarm_email_endpoints" {
  description = "Email addresses subscribed to operational alarm notifications. Recipients must confirm their subscriptions."
  type        = set(string)
  default     = []
}

variable "sentry_environment" {
  description = "Sentry environment tag (e.g. prod, staging)."
  type        = string
  default     = ""
}

variable "secrets_revision" {
  description = "Bump after rotating an SSM secret to force new Lambda instances that reload secrets."
  type        = string
  default     = "1"
}
