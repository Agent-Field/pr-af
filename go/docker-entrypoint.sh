#!/bin/sh
# Generate the opencode config at container start so PR_AF_MODEL is honored.
#
# Previously opencode.json was baked into the image with a hardcoded model and
# a single-model provider whitelist. That meant PR_AF_MODEL was ignored by the
# opencode harness: even though the model is passed via `-m`, opencode fell back
# to (and restricted itself to) the baked model. Generating the config here from
# PR_AF_MODEL fixes that — the env var wins when set, and we fall back to the
# benchmarked default when it isn't.
set -e

# Default matches the image ENV / benchmarked model.
MODEL="${PR_AF_MODEL:-deepseek/deepseek-v4-flash-0731}"

# opencode keys models under a provider by the slug *without* the provider
# prefix, e.g. "openrouter/z-ai/glm-5.2" -> provider "openrouter", key "z-ai/glm-5.2".
MODEL_KEY="${MODEL#openrouter/}"

CONFIG_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/opencode"
mkdir -p "$CONFIG_DIR"

cat > "$CONFIG_DIR/opencode.json" <<EOF
{"\$schema":"https://opencode.ai/config.json","model":"${MODEL}","small_model":"${MODEL}","provider":{"openrouter":{"options":{"apiKey":"{env:OPENROUTER_API_KEY}"},"models":{"${MODEL_KEY}":{}}}}}
EOF

exec "$@"
