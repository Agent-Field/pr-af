FROM python:3.11-slim AS builder

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1

WORKDIR /app

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    git && \
    rm -rf /var/lib/apt/lists/*

COPY pyproject.toml README.md ./
COPY src/ src/

RUN pip install --no-cache-dir --prefix=/install \
    "agentfield>=0.1.83" \
    "pydantic>=2.0" \
    "httpx>=0.27" \
    "python-dotenv>=1.0" \
    "fastapi>=0.100" \
    "uvicorn>=0.20" \
    "PyJWT[crypto]>=2.8" \
    "claude-agent-sdk>=0.1" && \
    pip install --no-cache-dir --prefix=/install --no-deps .


FROM python:3.11-slim AS runtime

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    AGENTFIELD_SERVER=http://agentfield:8080 \
    HARNESS_PROVIDER=opencode \
    HARNESS_MODEL=openrouter/moonshotai/kimi-k2.6 \
    AI_MODEL=openrouter/moonshotai/kimi-k2.6 \
    PORT=8004 \
    HOME=/home/praf \
    PYTHONPATH=/app/src \
    PATH=/home/praf/.opencode/bin:/usr/local/share/npm-global/bin:${PATH} \
    XDG_DATA_HOME=/home/praf/.local/share \
    PR_AF_WORKDIR=/workspaces \
    MALLOC_TRIM_THRESHOLD_=0

WORKDIR /app

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    git \
    nodejs \
    npm && \
    npm install -g @anthropic-ai/claude-code --prefix /usr/local/share/npm-global && \
    groupadd --gid 10001 praf && \
    useradd --uid 10001 --gid praf --no-create-home --home-dir /home/praf --shell /bin/sh praf && \
    mkdir -p /workspaces /home/praf/.local/share /home/praf/.opencode/data /home/praf/.claude && \
    echo '{"hasCompletedOnboarding":true}' > /home/praf/.claude.json && \
    chown -R praf:praf /home/praf /app /workspaces && \
    su -s /bin/sh praf -c "curl -fsSL https://opencode.ai/install | bash" && \
    rm -rf /var/lib/apt/lists/*

RUN mkdir -p /home/praf/.config/opencode && \
    echo '{"$schema":"https://opencode.ai/config.json","model":"{env:HARNESS_MODEL}","small_model":"{env:HARNESS_MODEL}","provider":{"openrouter":{"options":{"apiKey":"{env:OPENROUTER_API_KEY}"}}}}' \
    > /home/praf/.config/opencode/opencode.json && \
    chown -R praf:praf /home/praf/.config

COPY --from=builder /install /usr/local
COPY src/ /app/src/

USER praf

EXPOSE 8004

HEALTHCHECK --interval=30s --timeout=5s --retries=3 \
    CMD curl -f http://localhost:8004/health || exit 1

CMD ["python", "-m", "pr_af.app"]
