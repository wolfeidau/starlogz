# Local Lambda validation (SAM)

**For local testing and validation only.** Production infrastructure is
Terraform in `infra/terraform`, deployed with `bin/deploy`. Nothing in this
directory is deployed.

`sam local start-api` runs the real Lambda package (`infra/lambda/bootstrap`
plus a Linux `starlogz-server`) in the `provided.al2023` image under the Lambda
Runtime Interface Emulator, behind a local emulation of an API Gateway HTTP API
that sends payload format 2.0 events. This exercises the `lambda` command, the
runtime loop, and the `internal/lambdahttp` adapter end to end.

## Usage

Requires Docker, SAM CLI, `jq`, the docker compose Postgres, and the variables
from `.envrc` (`GITHUB_CLIENT_ID`, `GITHUB_CLIENT_SECRET`,
`TOKEN_ENCRYPTION_KEY`, `DATABASE_URL`).

```bash
docker compose up -d postgres
mise exec -- infra/sam/run      # builds, then serves on http://127.0.0.1:3000
mise exec -- infra/sam/smoke    # in another shell
```

`run` builds for the host architecture, rewrites `localhost` in
`DATABASE_URL` to `host.docker.internal`, and writes generated env values,
including secrets, to the gitignored `.build/env.json`. Sentry is not
configured, so local runs send no events.

## Not covered

The local API Gateway emulation is not exact. Confirm these after a deploy:
throttling, custom domain and TLS, `x-forwarded-*` headers and the `Secure`
cookie flag, cold-start timings (local container start dominates), and the
SIGTERM shutdown flush.
