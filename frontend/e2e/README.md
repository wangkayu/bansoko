# E2E Browser Tests

These tests use Playwright and exercise the built/current UI.

## Running

```bash
cd frontend
pnpm test:e2e
```

To run against an already-started server:

```bash
BANSOKO_TEST_BASE_URL=http://localhost:8080 pnpm test:e2e
```

## Authentication

Tests that require a logged-in session read credentials from environment variables:

- `BANSOKO_E2E_USERNAME`
- `BANSOKO_E2E_PASSWORD`

If these are not set, tests requiring authentication are skipped with a message.
The auth setup project (`auth.setup.ts`) logs in once and saves storage state so
other tests can reuse the session.
