# Contributing to PR-AF

Thanks for considering a contribution. PR-AF is open-source, and we welcome bug reports, fixes, new reasoners, and documentation improvements.

## Getting set up

PR-AF is a Python 3.11+ application that runs on top of [AgentField](https://github.com/Agent-Field/agentfield). The simplest path is the docker-compose flow described in the [README](README.md), which brings up an AgentField control plane and PR-AF in one step.

If you want to run from source:

```bash
git clone https://github.com/Agent-Field/pr-af.git
cd pr-af
python -m venv .venv && source .venv/bin/activate
pip install -e ".[dev]"

cp .env.example .env       # set OPENROUTER_API_KEY and GH_TOKEN at minimum
af                         # start the AgentField control plane in another terminal
python main.py             # start PR-AF
```

## Running tests and linting

```bash
make test          # pytest tests/
make check         # tests + bytecode compile + ruff
make clean         # remove caches and build artifacts
```

CI runs ruff against `src/` and `scripts/` and validates the Docker build on every push. The test suite runs locally via `make test` (not in CI yet — see the issue tracker if you want to help wire it up). PRs that fail CI will not be merged until green.

## What makes a good PR

- **One concern per PR.** A bug fix and a refactor should be two PRs.
- **Update tests.** New behavior needs a test; bug fixes need a regression test.
- **Don't break the public API.** `pr-af.review` and the AgentField reasoners it exposes are part of the contract. If you need to change them, flag it in the PR description.
- **Keep secrets out of the diff.** No tokens, API keys, or webhook secrets committed to the repo or pasted in PR descriptions.
- **Match existing style.** Run ruff before pushing.

## Architecture changes

PR-AF is a 7-phase pipeline (intake → anatomy → planning → review → review-layer → synthesis → output). If you're touching the orchestrator, the harness definitions, or adding a new phase, please:

1. Read [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) first.
2. Open a draft PR or issue early to discuss the design before implementing.
3. Document the change in [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) and add an entry to [CHANGELOG.md](CHANGELOG.md).

## Reporting issues

- **Bugs:** open a GitHub issue with steps to reproduce, the PR-AF version (or commit), the repo/PR you ran it against, and any logs (with secrets redacted).
- **Security:** see [SECURITY.md](SECURITY.md) — please use private vulnerability reporting, not public issues.

## Code of Conduct

By participating in this project you agree to follow the [Code of Conduct](CODE_OF_CONDUCT.md).
