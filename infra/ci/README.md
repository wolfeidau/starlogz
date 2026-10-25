# infra/ci

IAM role `starlogz-<env>-buildkite-deploy`, assumed by the `starlogz-deploy`
Buildkite pipeline via OIDC (`main` branch only). The OIDC provider is created
by a separate CloudFormation stack and looked up here.

This is a separate Terraform root from `infra/terraform/` so the CI role cannot
edit its own permissions: the role has no IAM write access and cannot touch this
root's state. Only an admin applies this root.

## One-time admin apply

The repository is public, so identifiers stay out of it. Create the gitignored
`infra/ci/terraform.tfvars` with the Buildkite organization and `starlogz-deploy`
pipeline UUIDs (Buildkite API or pipeline settings):

```hcl
buildkite_organization_id = "<org uuid>"
buildkite_pipeline_id     = "<starlogz-deploy pipeline uuid>"
```

```bash
BUCKET=$(aws ssm get-parameter --region us-west-2 --name /config/dev/master/terraform_bucket --query Parameter.Value --output text)
terraform -chdir=infra/ci init -backend-config="bucket=${BUCKET}"
terraform -chdir=infra/ci plan -out ci.tfplan
terraform -chdir=infra/ci apply ci.tfplan
```

The `role_arn` output is the role Buildkite assumes; set it as
`STARLOGZ_DEPLOY_ROLE_ARN` in the `starlogz-deploy` pipeline settings (see
`infra/README.md`).
