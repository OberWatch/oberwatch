# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability in Oberwatch, please report it responsibly.

**Do NOT open a public GitHub issue for security vulnerabilities.**

Instead, please email:

**security@oberwatch.com**

Include:
- Description of the vulnerability
- Steps to reproduce
- Potential impact
- Suggested fix (if any)

## Response Timeline

- **Acknowledgment**: Within 48 hours
- **Initial assessment**: Within 1 week
- **Fix and disclosure**: Coordinated with the reporter

## Scope

This policy applies to the Oberwatch codebase and official Docker images. Third-party integrations and forks are out of scope.

## Security Design

- Oberwatch does not store or log prompt content by default (privacy-first)
- API keys are passed through to upstream providers and are not stored unless explicitly configured
- The dashboard is protected by a configurable auth token
- All agent-to-Oberwatch communication should use TLS in production
