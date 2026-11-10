#!/bin/bash
# =============================================================================
# Configura as chaves do New Relic, OpsGenie e Discord no Kubernetes
# =============================================================================
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

if [ -f "${ROOT_DIR}/.env.observability" ]; then
  set -a
  # shellcheck disable=SC1091
  source "${ROOT_DIR}/.env.observability"
  set +a
fi

NAMESPACE="monitoring"
NEW_RELIC_ENDPOINT="${NEW_RELIC_OTLP_ENDPOINT:-https://otlp.nr-data.net:4318}"

if [ -z "${NEW_RELIC_LICENSE_KEY}" ] || [ -z "${OPSGENIE_API_KEY}" ] || [ -z "${DISCORD_WEBHOOK_URL}" ]; then
  echo "Erro: Defina NEW_RELIC_LICENSE_KEY, OPSGENIE_API_KEY e DISCORD_WEBHOOK_URL no .env.observability ou no ambiente."
  exit 1
fi

echo "=== Atualizando Secret observability-secrets no namespace ${NAMESPACE} ==="
kubectl create namespace "${NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -

kubectl create secret generic observability-secrets \
  --namespace "${NAMESPACE}" \
  --from-literal=NEW_RELIC_LICENSE_KEY="${NEW_RELIC_LICENSE_KEY}" \
  --from-literal=NEW_RELIC_OTLP_ENDPOINT="${NEW_RELIC_ENDPOINT}" \
  --from-literal=OPSGENIE_API_KEY="${OPSGENIE_API_KEY}" \
  --from-literal=DISCORD_WEBHOOK_URL="${DISCORD_WEBHOOK_URL}" \
  --from-literal=GITHUB_TOKEN="${GITHUB_TOKEN:-}" \
  --dry-run=client -o yaml | kubectl apply -f -

echo "=== Reiniciando OTel Collector, Self-Healing Service e Grafana para aplicar credenciais ==="
kubectl rollout restart deployment/otel-collector -n "${NAMESPACE}" 2>/dev/null || true
kubectl rollout restart deployment/self-healing-service -n "${NAMESPACE}" 2>/dev/null || true
kubectl rollout restart deployment/kube-prometheus-stack-grafana -n "${NAMESPACE}" 2>/dev/null || true

echo "=== Credenciais aplicadas com sucesso! ==="
