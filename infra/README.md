# Deploying starlogz

## How CI deploys dev

A passing `main` build of the `starlogz` pipeline triggers the `starlogz-deploy`
pipeline (`.buildkite/pipeline.yml` async trigger step, pull requests excluded). The
deploy pipeline is defined in `.buildkite/deploy.yml` and runs one build at a
time (`concurrency_group: starlogz-deploy-dev`):

1. `verify` fetches `main` from `origin` and fails unless the triggering commit is an
   ancestor of it. The `starlogz` pipeline is public, so a pull request could
   otherwise edit `pipeline.yml` to deploy an unmerged commit.
2. `approve` is a block step: someone must click it in Buildkite before
   anything deploys. The trigger is async, so the `starlogz` build finishes
   without waiting. Approve only the newest pending deploy; approving an older
   one after a newer one ships rolls the Lambda back.
3. `deploy` assumes `starlogz-dev-buildkite-deploy` through Buildkite OIDC,
   writes `infra/terraform/terraform.tfvars` from the `STARLOGZ_DEV_TFVARS`
   secret, takes `DATABASE_URL` from `STARLOGZ_MIGRATE_DATABASE_URL`, and runs
   `bin/deploy dev`.

`bin/deploy` builds, uploads the zip, plans, migrates, then applies. Under
`CI=true` it does not prompt. If `DATABASE_URL` is set it migrates with that URL
(and refuses port 6432) instead of loading secrets from SSM.

### Plan guard

After `terraform plan`, `bin/deploy` inspects the plan JSON. It allows only:

- no-op for every managed resource, and
- an `update` of `aws_lambda_function.starlogz`.

Anything else (other resources, or a Lambda replace/delete) fails the CI deploy
with the offending addresses listed. Run locally, it prints them and asks for
confirmation. Apply infrastructure changes by running `bin/deploy` or terraform
locally.

## One-time setup

### AWS: Buildkite OIDC provider

Account-wide; deploy once per account with admin credentials. Roles trusting it
must pin `agent.buildkite.com:organization_id`, because every Buildkite
customer shares the issuer.

```bash
aws cloudformation deploy --region us-west-2 \
  --stack-name buildkite-oidc-provider \
  --template-file infra/cfn/buildkite-oidc-provider.yaml \
  --parameter-overrides Stage=dev \
  --tags application=buildkite-oidc environment=dev
```

### AWS: deploy role

Apply `infra/ci` with admin credentials after the provider exists; see
`infra/ci/README.md`. The role trusts only `starlogz-deploy` builds of `main`
in this organization and cannot read application secrets in SSM.

`.buildkite/deploy.yml` reads the role ARN from `STARLOGZ_DEPLOY_ROLE_ARN` so the
account ID stays out of this public repository. Set it as a pipeline-level `env`
in the `starlogz-deploy` pipeline's settings YAML (the pipeline is private):

```yaml
env:
  STARLOGZ_DEPLOY_ROLE_ARN: "<role_arn output from infra/ci>"
steps:
  - label: ":pipeline:"
    command: "buildkite-agent pipeline upload .buildkite/deploy.yml"
    agents:
      queue: hosted
```

### Buildkite cluster secrets

Create these cluster secrets, each with the access policy below:

- `STARLOGZ_MIGRATE_DATABASE_URL`: PlanetScale Postgres URL for the migration
  role. Use the direct port 5432 with `sslmode=verify-full`. PgBouncer (6432)
  breaks the migration runner's session advisory lock and `CREATE INDEX
  CONCURRENTLY`.
- `STARLOGZ_DEV_TFVARS`: full contents of `infra/terraform/terraform.tfvars`.
  The file is gitignored and contains a personal email; never commit it.

```yaml
- pipeline_slug: starlogz-deploy
  build_branch: main
```

### PlanetScale migration role

Create a user-defined role for migrations in the PlanetScale dashboard. The
migrator owns the schema objects; the app role gets only data access. Then, as
the default (admin) role, run the following with psql variables
(`psql -v app_role=... -v migrator_role=... -f grants.sql`). Do this before the
first CI deploy, then smoke-test the app.

```sql
REASSIGN OWNED BY :"app_role" TO :"migrator_role";

GRANT USAGE ON SCHEMA public TO :"app_role";
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO :"app_role";
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO :"app_role";

ALTER DEFAULT PRIVILEGES FOR ROLE :"migrator_role" IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO :"app_role";
ALTER DEFAULT PRIVILEGES FOR ROLE :"migrator_role" IN SCHEMA public
  GRANT USAGE, SELECT ON SEQUENCES TO :"app_role";
```

Migrations also create functions (`immutable_text_array_to_string`, the
`audit_*_trigger_func` family). PostgreSQL grants `EXECUTE` on functions to
`PUBLIC` by default, so no extra grant is needed unless that default has been
revoked. If it has, add `GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA public` and a
matching `ALTER DEFAULT PRIVILEGES ... GRANT EXECUTE ON FUNCTIONS`.
