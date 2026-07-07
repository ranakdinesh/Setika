# Setika UI

Next.js and Tailwind UI for testing Setika API login and dashboard flows.

## Run

```bash
npm install
npm run dev
```

The app defaults to `http://localhost:8086`. Override it with:

```bash
NEXT_PUBLIC_SETIKA_API_BASE=http://localhost:8086
```

## Included Screens

- Login page wired to `POST /setika/auth/login`
- Dashboard with token claim preview
- Endpoint probe table for health, readiness, admin tenants, HRMS, CRM, and course candidates
- HRMS employee directory preview adapted from the Spur UI SmartHR conversion work
