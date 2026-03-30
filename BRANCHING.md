# Branching Strategy

Oberwatch uses a protected `staging` branch in front of `main`.

## Branch Overview

- `main` is the beta / pre-production branch. Every merge to `main` should be stable enough for preview testing and eligible for a tagged release.
- `staging` is the integration branch. Feature work lands here first for combined testing.
- `feature/*` branches are short-lived branches created from `staging` for normal work.
- `hotfix/*` branches are short-lived branches created from `main` for urgent fixes that must be promoted quickly toward the next beta or tagged release.

## Feature Workflow

Create each feature branch from `staging`, not from `main`.

```bash
git checkout staging
git pull origin staging
git checkout -b feature/short-description
```

Develop on the feature branch, push it, and open a pull request targeting `staging`.

```bash
git push origin feature/short-description
```

Every pull request to `staging` must pass CI before merge.

## Promoting Staging to Main

When `staging` is stable, open a pull request from `staging` to `main`.

- Merge to `main` only after CI passes.
- Use a merge commit so `main` preserves the exact tested integration state from `staging`.
- Treat every merge to `main` as beta / pre-production ready, not as the final stable release.

Merges to `main` publish the `beta` container image. Merges to `staging` publish the `staging` container image.

## Tagged Releases

Tagged releases are created from `main` only.

- Create a tag like `v0.1.0` on `main`.
- Pushing the tag runs the release workflow.
- The release workflow publishes binaries plus stable container images to GHCR and Docker Hub.
- Container tags include the full version, the minor version alias, and `latest`.

## Hotfix Workflow

Hotfixes start from `main` because they address production issues.

```bash
git checkout main
git pull origin main
git checkout -b hotfix/short-description
```

Open the hotfix pull request against `main`. After it merges, cherry-pick the same fix onto `staging` so the branches stay aligned.

## Summary Rules

- Create `feature/*` branches from `staging`.
- Open normal pull requests into `staging`.
- Use squash merge for normal feature PRs into `staging`.
- Promote `staging` into `main` when the integration branch is stable.
- Use a merge commit for `staging` -> `main` promotion PRs.
- Create release tags from `main` only.
- Create `hotfix/*` branches from `main`, merge them to `main`, then backport to `staging`.
