#!/bin/sh
set -e

# Single source of truth: HARNESS_MODEL env var drives opencode config
MODEL="${HARNESS_MODEL:-openrouter/minimax/minimax-m2.5}"

# Extract the provider/model path (strip "openrouter/" prefix for the models map key)
MODEL_KEY="${MODEL#openrouter/}"

mkdir -p "$HOME/.config/opencode"
cat > "$HOME/.config/opencode/opencode.json" <<EOF
{
  "\$schema": "https://opencode.ai/config.json",
  "model": "${MODEL}",
  "small_model": "${MODEL}",
  "provider": {
    "openrouter": {
      "options": {
        "apiKey": "{env:OPENROUTER_API_KEY}"
      },
      "models": {
        "${MODEL_KEY}": {}
      }
    }
  }
}
EOF

exec "$@"
