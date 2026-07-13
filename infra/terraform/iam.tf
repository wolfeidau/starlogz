data "aws_iam_policy_document" "lambda_assume_role" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "lambda" {
  name               = "${local.name_prefix}-lambda"
  assume_role_policy = data.aws_iam_policy_document.lambda_assume_role.json
}

resource "aws_iam_role_policy_attachment" "basic_execution" {
  role       = aws_iam_role.lambda.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"
}

data "aws_iam_policy_document" "lambda_s3" {
  statement {
    actions   = ["s3:GetObject"]
    resources = ["${aws_s3_bucket.deploy.arn}/*"]
  }
}

resource "aws_iam_role_policy" "lambda_s3" {
  name   = "s3-deploy-bucket"
  role   = aws_iam_role.lambda.id
  policy = data.aws_iam_policy_document.lambda_s3.json
}

data "aws_iam_policy_document" "lambda_wide_events" {
  statement {
    actions   = ["events:PutEvents"]
    resources = [aws_cloudwatch_event_bus.wide_events.arn]
  }
}

resource "aws_iam_role_policy" "lambda_wide_events" {
  name   = "eventbridge-wide-events"
  role   = aws_iam_role.lambda.id
  policy = data.aws_iam_policy_document.lambda_wide_events.json
}
