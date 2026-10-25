variable "env" {
  description = "Environment name (e.g. dev, prod)."
  type        = string
  default     = "dev"
}

variable "aws_region" {
  description = "AWS region to deploy into."
  type        = string
  default     = "us-west-2"
}

variable "branch" {
  description = "Git branch this deployment was made from (tagging only)."
  type        = string
  default     = "main"
}

variable "buildkite_organization_id" {
  description = "Buildkite organization UUID allowed to assume the deploy role."
  type        = string
}

variable "buildkite_pipeline_id" {
  description = "Buildkite pipeline UUID (starlogz-deploy) allowed to assume the deploy role."
  type        = string
}

variable "deploy_branch" {
  description = "Only builds of this branch may assume the deploy role."
  type        = string
  default     = "main"
}
