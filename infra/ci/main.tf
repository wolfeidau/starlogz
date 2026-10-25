terraform {
  # use_lockfile (S3-native state locking) requires 1.10.
  required_version = ">= 1.10.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }

  # bucket is supplied at init via -backend-config="bucket=...".
  backend "s3" {
    key          = "dev/main/starlogz-ci.tfstate"
    region       = "us-west-2"
    encrypt      = true
    use_lockfile = true
  }
}

provider "aws" {
  region = var.aws_region

  default_tags {
    tags = {
      application = "starlogz"
      environment = var.env
      branch      = var.branch
      component   = "ci"
    }
  }
}

data "aws_caller_identity" "current" {}

# CloudFormation-owned; resolved at plan time so the bucket name stays out of the repo.
data "aws_ssm_parameter" "state_bucket" {
  name = "/config/${var.env}/master/terraform_bucket"
}

# Created by a separate CloudFormation stack.
data "aws_iam_openid_connect_provider" "buildkite" {
  url = "https://agent.buildkite.com"
}

locals {
  name_prefix   = "starlogz-${var.env}"
  deploy_bucket = "starlogz-deploy-${var.env}"
  account_id    = data.aws_caller_identity.current.account_id
}
