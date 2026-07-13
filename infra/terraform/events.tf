data "aws_caller_identity" "current" {}

resource "aws_cloudwatch_event_bus" "wide_events" {
  name = local.name_prefix
}

resource "aws_cloudwatch_log_group" "wide_events" {
  name              = "/aws/events/${local.name_prefix}"
  retention_in_days = 90
}

resource "aws_cloudwatch_event_rule" "wide_events" {
  name           = "${local.name_prefix}-wide-events"
  description    = "Routes privacy-safe Starlogz core-flow events to CloudWatch Logs."
  event_bus_name = aws_cloudwatch_event_bus.wide_events.name
  event_pattern = jsonencode({
    source = ["starlogz.service"]
  })
}

data "aws_iam_policy_document" "wide_event_logs" {
  statement {
    actions   = ["logs:CreateLogStream", "logs:PutLogEvents"]
    resources = ["${aws_cloudwatch_log_group.wide_events.arn}:*"]

    principals {
      type        = "Service"
      identifiers = ["events.amazonaws.com", "delivery.logs.amazonaws.com"]
    }

    condition {
      test     = "StringEquals"
      variable = "AWS:SourceAccount"
      values   = [data.aws_caller_identity.current.account_id]
    }

    condition {
      test     = "ArnEquals"
      variable = "AWS:SourceArn"
      values   = [aws_cloudwatch_event_rule.wide_events.arn]
    }
  }
}

resource "aws_cloudwatch_log_resource_policy" "wide_events" {
  policy_name     = "${local.name_prefix}-wide-events"
  policy_document = data.aws_iam_policy_document.wide_event_logs.json
}

resource "aws_cloudwatch_event_target" "wide_event_logs" {
  event_bus_name = aws_cloudwatch_event_bus.wide_events.name
  rule           = aws_cloudwatch_event_rule.wide_events.name
  target_id      = "cloudwatch-logs"
  arn            = aws_cloudwatch_log_group.wide_events.arn

  depends_on = [aws_cloudwatch_log_resource_policy.wide_events]
}
