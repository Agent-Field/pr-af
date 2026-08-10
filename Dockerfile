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
    "agentfield==0.1.126" \
    "hax-sdk>=0.2.4" \
    "pydantic>=2.0" \
    "httpx>=0.27" \
    "python-dotenv>=1.0" \
    "fastapi>=0.100" \
    "uvicorn>=0.20" \
    "PyJWT[crypto]>=2.8" && \
    pip install --no-cache-dir --prefix=/install --no-deps .


FROM python:3.11-slim AS runtime

ARG OPENCODE_VERSION=1.17.15

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    AGENTFIELD_SERVER=http://agentfield:8080 \
    PR_AF_PROVIDER=opencode \
    PR_AF_MODEL=openrouter/moonshotai/kimi-k2.5 \
    PORT=8004 \
    HOME=/home/praf \
    PYTHONPATH=/app/src \
    PATH=/home/praf/.opencode/bin:${PATH} \
    XDG_DATA_HOME=/home/praf/.local/share \
    PR_AF_WORKDIR=/workspaces

WORKDIR /app

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    git && \
    groupadd --gid 10001 praf && \
    useradd --uid 10001 --gid praf --create-home --home-dir /home/praf --shell /bin/sh praf && \
    su -s /bin/sh praf -c "curl -fsSL https://opencode.ai/install | bash -s -- --version ${OPENCODE_VERSION} --no-modify-path" && \
    mkdir -p /workspaces /home/praf/.local/share && \
    chown -R praf:praf /app /workspaces /home/praf && \
    rm -rf /var/lib/apt/lists/*

COPY --from=builder /install /usr/local
COPY src/ /app/src/
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh

USER praf

EXPOSE 8004

HEALTHCHECK --interval=30s --timeout=5s --retries=3 \
    CMD curl -f http://localhost:8004/health || exit 1

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["python", "-m", "pr_af.app"]
