terraform {
  # use_lockfile (S3-native state locking) requires 1.10.
  required_version = ">= 1.10.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }

  # bucket is supplied by bin/tf-init from SSM.
  backend "s3" {
    key          = "dev/main/starlogz.tfstate"
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
      component   = var.component
    }
  }
}

locals {
  name_prefix      = "starlogz-${var.env}"
  deploy_bucket    = "starlogz-deploy-${var.env}"
  hostname_prefix  = var.env == "prod" ? "starlogz" : "starlogz-${var.env}"
  service_hostname = "${local.hostname_prefix}.${var.domain}"
  server_url       = "https://${local.service_hostname}"
}
