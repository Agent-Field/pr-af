PYTHON ?= python3

.PHONY: test check lint clean

test:
	$(PYTHON) -m pytest tests/ -x -q

lint:
	$(PYTHON) -m ruff check src/ scripts/

check: lint test
	$(PYTHON) -m compileall -q src/pr_af/

clean:
	find . -path "./.git" -prune -o -path "./.venv" -prune -o -type f \( -name "*.pyc" -o -name ".DS_Store" -o -name "*.bak" \) -delete
	find . -path "./.git" -prune -o -path "./.venv" -prune -o -depth -type d -name "__pycache__" -empty -delete
	rm -rf .pytest_cache .ruff_cache .mypy_cache .coverage htmlcov src/*.egg-info
