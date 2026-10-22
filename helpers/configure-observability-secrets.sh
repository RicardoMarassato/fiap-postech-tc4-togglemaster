#!/bin/bash
# =============================================================================
# Configura as chaves do New Relic, PagerDuty e Discord no Kubernetes
# =============================================================================
# Uso:
#   export NEW_RELIC_LICENSE_KEY="sua_chave_nral"
#   export PAGERDUTY_INTEGRATION_KEY="sua_integration_key_pagerduty"
#   export DISCORD_WEBHOOK_URL="https://discord.com/api/webhooks/..."
#   ./helpers/configure-observability-secrets.sh
# =============================================================================
set -e

NAMESPACE="monitoring"
NEW_RELIC_ENDPOINT="${NEW_RELIC_OTLP_ENDPOINT:-https://otlp.nr-data.net:4318}"

if [ -z "${NEW_RELIC_LICENSE_KEY}" ] || [ -z "${PAGERDUTY_INTEGRATION_KEY}" ] || [ -z "${DISCORD_WEBHOOK_URL}" ]; then
  echo "Uso:"
  echo "  export NEW_RELIC_LICENSE_KEY='...NRAL'"
  echo "  export PAGERDUTY_INTEGRATION_KEY='...'"
  echo "  export DISCORD_WEBHOOK_URL='https://discord.com/api/webhooks/...'"
  echo "  ./helpers/configure-observability-secrets.sh"
  exit 1
fi

echo "=== Atualizando Secret observability-secrets no namespace ${NAMESPACE} ==="
kubectl create namespace "${NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -

kubectl create secret generic observability-secrets \
  --namespace "${NAMESPACE}" \
  --from-literal=NEW_RELIC_LICENSE_KEY="${NEW_RELIC_LICENSE_KEY}" \
  --from-literal=NEW_RELIC_OTLP_ENDPOINT="${NEW_RELIC_ENDPOINT}" \
  --from-literal=PAGERDUTY_INTEGRATION_KEY="${PAGERDUTY_INTEGRATION_KEY}" \
  --from-literal=DISCORD_WEBHOOK_URL="${DISCORD_WEBHOOK_URL}" \
  --from-literal=GITHUB_TOKEN="${GITHUB_TOKEN:-}" \
  --dry-run=client -o yaml | kubectl apply -f -

echo "=== Reiniciando OTel Collector, Self-Healing Service e Grafana para aplicar credenciais ==="
kubectl rollout restart deployment/otel-collector -n "${NAMESPACE}" 2>/dev/null || true
kubectl rollout restart deployment/self-healing-service -n "${NAMESPACE}" 2>/dev/null || true
kubectl rollout restart deployment/kube-prometheus-stack-grafana -n "${NAMESPACE}" 2>/dev/null || true

echo "=== Credenciais aplicadas com sucesso! ==="
