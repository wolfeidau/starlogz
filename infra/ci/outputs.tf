output "role_arn" {
  description = "ARN of the role Buildkite assumes to deploy."
  value       = aws_iam_role.deploy.arn
}
