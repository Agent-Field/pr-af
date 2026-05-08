# Changelog

All notable changes to this project will be documented in this file.

The format is based on Keep a Changelog, and this project follows Semantic Versioning.

## [Unreleased]

### Added

- Public release scaffolding: LICENSE (Apache 2.0), CODE_OF_CONDUCT, SECURITY, CONTRIBUTING, CHANGELOG, CODEOWNERS, .dockerignore, Makefile.
- Expanded README with installation, environment-variable reference, and troubleshooting.

## [0.1.0] - 2026-05-08

### Added

- Initial public release of PR-AF.
- 7-phase agentic PR review pipeline: intake → anatomy → planning (3 parallel meta-phases) → review (N parallel reviewer agents) → review-layer (cross-ref resolver, adversary, coverage gate) → synthesis (compound finding clustering) → output.
- AgentField reasoner `pr-af.review` for invocation via `app.call(...)` or the AgentField REST API.
- GitHub PR URL, raw diff, and local-repo input modes.
- Inline GitHub review comment posting with evidence anchors.
- AI-PR detection and adaptive review depth.
- OpenCode harness with multi-provider model support (OpenRouter, Claude, etc.).
- GitHub Actions integration example.
