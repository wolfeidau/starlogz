locals {
  oidc_host        = "agent.buildkite.com"
  lambda_arn       = "arn:aws:lambda:${var.aws_region}:${local.account_id}:function:${local.name_prefix}"
  lambda_role_arn  = "arn:aws:iam::${local.account_id}:role/${local.name_prefix}-lambda"
  state_bucket_arn = "arn:aws:s3:::${data.aws_ssm_parameter.state_bucket.value}"
  state_key        = "dev/main/starlogz.tfstate"
}

data "aws_iam_policy_document" "deploy_assume_role" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]

    principals {
      type        = "Federated"
      identifiers = [data.aws_iam_openid_connect_provider.buildkite.arn]
    }

    condition {
      test     = "StringEquals"
      variable = "${local.oidc_host}:aud"
      values   = ["sts.amazonaws.com"]
    }

    condition {
      test     = "StringEquals"
      variable = "${local.oidc_host}:organization_id"
      values   = [var.buildkite_organization_id]
    }

    condition {
      test     = "StringEquals"
      variable = "${local.oidc_host}:pipeline_id"
      values   = [var.buildkite_pipeline_id]
    }

    condition {
      test     = "StringEquals"
      variable = "${local.oidc_host}:build_branch"
      values   = [var.deploy_branch]
    }

    # IAM rejects agent.buildkite.com trust without a scoped sub; this also limits it to the deploy step.
    condition {
      test     = "StringLike"
      variable = "${local.oidc_host}:sub"
      values   = ["organization:${var.buildkite_organization_slug}:pipeline:${var.buildkite_pipeline_slug}:ref:refs/heads/${var.deploy_branch}:commit:*:step:${var.deploy_step_key}"]
    }
  }
}

resource "aws_iam_role" "deploy" {
  name                 = "${local.name_prefix}-buildkite-deploy"
  assume_role_policy   = data.aws_iam_policy_document.deploy_assume_role.json
  max_session_duration = 3600
}

data "aws_iam_policy_document" "deploy" {
  # GetObject: lambda:UpdateFunctionCode from S3 uses the caller's permissions.
  statement {
    sid       = "ArtifactObjects"
    actions   = ["s3:PutObject", "s3:GetObject"]
    resources = ["arn:aws:s3:::${local.deploy_bucket}/*"]
  }

  statement {
    sid = "DeployBucketRefresh"
    actions = [
      "s3:ListBucket",
      "s3:GetBucket*",
      "s3:GetAccelerateConfiguration",
      "s3:GetEncryptionConfiguration",
      "s3:GetLifecycleConfiguration",
      "s3:GetReplicationConfiguration",
    ]
    resources = ["arn:aws:s3:::${local.deploy_bucket}"]
  }

  statement {
    sid = "LambdaDeploy"
    actions = [
      "lambda:UpdateFunctionCode",
      "lambda:UpdateFunctionConfiguration",
      "lambda:Get*",
      "lambda:List*",
    ]
    resources = [local.lambda_arn, "${local.lambda_arn}:*"]
  }

  statement {
    sid       = "PassLambdaRole"
    actions   = ["iam:PassRole"]
    resources = [local.lambda_role_arn]

    condition {
      test     = "StringEquals"
      variable = "iam:PassedToService"
      values   = ["lambda.amazonaws.com"]
    }
  }

  statement {
    sid = "LambdaRoleRefresh"
    actions = [
      "iam:GetRole",
      "iam:GetRolePolicy",
      "iam:ListRolePolicies",
      "iam:ListAttachedRolePolicies",
      "iam:ListRoleTags",
    ]
    resources = [local.lambda_role_arn]
  }

  # Unconditioned: the S3 backend lists workspaces under env:/ during init.
  statement {
    sid       = "StateList"
    actions   = ["s3:ListBucket"]
    resources = [local.state_bucket_arn]
  }

  statement {
    sid       = "StateObject"
    actions   = ["s3:GetObject", "s3:PutObject"]
    resources = ["${local.state_bucket_arn}/${local.state_key}"]
  }

  statement {
    sid       = "StateLock"
    actions   = ["s3:GetObject", "s3:PutObject", "s3:DeleteObject"]
    resources = ["${local.state_bucket_arn}/${local.state_key}.tflock"]
  }

  # Only the bucket name; CI must not read app secrets under /config/*/starlogz.
  statement {
    sid       = "StateBucketName"
    actions   = ["ssm:GetParameter"]
    resources = ["arn:aws:ssm:${var.aws_region}:${local.account_id}:parameter/config/${var.env}/master/terraform_bucket"]
  }

  # These read APIs mostly do not support resource-level scoping.
  statement {
    sid = "RefreshReadOnly"
    actions = [
      "acm:DescribeCertificate",
      "acm:ListTagsForCertificate",
      "apigateway:GET",
      "events:Describe*",
      "events:ListTagsForResource",
      "events:ListTargetsByRule",
      "logs:DescribeLogGroups",
      "logs:DescribeResourcePolicies",
      "logs:ListTagsForResource",
      "logs:ListTagsLogGroup",
      "cloudwatch:DescribeAlarms",
      "cloudwatch:ListTagsForResource",
      "route53:GetHostedZone",
      "route53:ListResourceRecordSets",
      "route53:GetChange",
      "route53:ListTagsForResource",
      "sns:GetTopicAttributes",
      "sns:GetSubscriptionAttributes",
      "sns:ListTagsForResource",
      "sns:GetDataProtectionPolicy",
      "sts:GetCallerIdentity",
    ]
    resources = ["*"]
  }
}

resource "aws_iam_role_policy" "deploy" {
  name   = "deploy"
  role   = aws_iam_role.deploy.id
  policy = data.aws_iam_policy_document.deploy.json
}
