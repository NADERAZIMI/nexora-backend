# Security Policy

## Reporting a vulnerability

Do not open a public issue for a security problem. Report it privately to the
security team at `security@nexora.example` (replace with the production contact
before launch) and include:

- Affected service, contract, or component.
- Reproduction steps or a proof of concept.
- Impact assessment.
- Whether customer funds, keys, or data may be affected.

We aim to acknowledge reports within one business day and to provide a
remediation timeline within five business days.

## Safe harbour

Good-faith research that avoids privacy violations, data destruction, service
disruption, and extortion is welcome. Do not test against production without
written authorization.

## Supported versions

Only the current main branch and the current production release are supported.
Security fixes are backported at the discretion of the security team.

## Engineering controls

- Secret scanning and dependency scanning run in CI.
- Production secrets are stored in a managed secret system, never in Git.
- Custody keys are held in HSM/MPC infrastructure, never in application memory.
- Access to production requires MFA, least privilege, and audit logging.

## Out of scope

- Findings in local development credentials that are clearly marked as such.
- Denial-of-service from unbounded local testing.
- Social engineering.
