terraform {
  required_version = ">= 1.5.7"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }

  # backend "s3" {
  #   bucket         = "your-tfstate-bucket"
  #   key            = "starlogz/${var.env}/terraform.tfstate"
  #   region         = "ap-southeast-2"
  #   dynamodb_table = "terraform-locks"
  #   encrypt        = true
  # }
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
  name_prefix       = "starlogz-${var.env}"
  deploy_bucket     = "starlogz-deploy-${var.env}"
  service_hostname  = "starlogz.${var.domain}"
  server_url        = "https://starlogz.${var.domain}"
}
