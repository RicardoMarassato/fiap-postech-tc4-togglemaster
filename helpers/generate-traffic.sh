#!/bin/bash
# =============================================================================
# Gerador de Tráfego e Rastreamento Distribuído (Distributed Tracing & Service Map)
# =============================================================================
# Este script cria uma API Key, uma Feature Flag e uma Regra de Segmentação,
# e dispara requisições contínuas no evaluation-service com ?nocache=true
# para exercitar os 5 microsserviços em cada requisição:
#   evaluation-service -> flag-service -> auth-service
#   evaluation-service -> targeting-service -> auth-service
#   evaluation-service -> SQS -> analytics-service -> DynamoDB
# =============================================================================
set -e

NAMESPACE="${NAMESPACE:-togglemaster}"
MASTER_KEY="${MASTER_KEY:-admin-secreto-123}"
FLAG_NAME="${FLAG_NAME:-checkout-v2}"
ITERATIONS="${1:-30}"

echo "=== Iniciando port-forward temporário para os serviços no namespace ${NAMESPACE} ==="
kubectl port-forward svc/auth-service -n "${NAMESPACE}" 18001:8001 >/dev/null 2>&1 &
PF_AUTH=$!
kubectl port-forward svc/flag-service -n "${NAMESPACE}" 18002:8002 >/dev/null 2>&1 &
PF_FLAG=$!
kubectl port-forward svc/targeting-service -n "${NAMESPACE}" 18003:8003 >/dev/null 2>&1 &
PF_TARGET=$!
kubectl port-forward svc/evaluation-service -n "${NAMESPACE}" 18004:8004 >/dev/null 2>&1 &
PF_EVAL=$!

cleanup() {
  kill ${PF_AUTH} ${PF_FLAG} ${PF_TARGET} ${PF_EVAL} 2>/dev/null || true
}
trap cleanup EXIT

sleep 3

echo "1. Criando/Validando API Key no auth-service..."
KEY_RESP=$(curl -s -X POST http://localhost:18001/admin/keys \
  -H "Authorization: Bearer ${MASTER_KEY}" \
  -H "Content-Type: application/json" \
  -d '{"name": "observability-test-key"}')
API_KEY=$(echo "${KEY_RESP}" | sed -n 's/.*"key":"\([^"]*\)".*/\1/p')

if [ -n "${API_KEY}" ]; then
  echo "   API Key gerada: ${API_KEY:0:15}..."
  # Atualiza a secret evaluation-service-secrets para que o evaluation-service use uma chave válida
  kubectl create secret generic evaluation-service-secrets \
    -n "${NAMESPACE}" \
    --from-literal=api-key="${API_KEY}" \
    --dry-run=client -o yaml | kubectl apply -f - >/dev/null
else
  echo "   Usando API Key existente do cluster..."
  API_KEY=$(kubectl get secret evaluation-service-secrets -n "${NAMESPACE}" -o jsonpath='{.data.api-key}' | base64 -d)
fi

echo "2. Garantindo que a Flag '${FLAG_NAME}' existe no flag-service..."
curl -s -X POST http://localhost:18002/flags \
  -H "Authorization: Bearer ${API_KEY}" \
  -H "Content-Type: application/json" \
  -d "{\"name\": \"${FLAG_NAME}\", \"description\": \"Nova interface de checkout instrumentada com OTel\", \"is_enabled\": true}" >/dev/null || true

echo "3. Garantindo que a Regra de Segmentação existe no targeting-service..."
curl -s -X POST http://localhost:18003/rules \
  -H "Authorization: Bearer ${API_KEY}" \
  -H "Content-Type: application/json" \
  -d "{\"flag_name\": \"${FLAG_NAME}\", \"is_enabled\": true, \"rules\": {\"type\": \"PERCENTAGE\", \"value\": 50}}" >/dev/null || true

echo "4. Disparando ${ITERATIONS} avaliações completas (Distributed Tracing nos 5 microsserviços)..."
for i in $(seq 1 "${ITERATIONS}"); do
  USER_ID="user-${i}"
  RESP=$(curl -s "http://localhost:18004/evaluate?user_id=${USER_ID}&flag_name=${FLAG_NAME}&nocache=true")
  echo "   [${i}/${ITERATIONS}] GET /evaluate?user_id=${USER_ID}&flag_name=${FLAG_NAME}&nocache=true -> ${RESP}"
  sleep 1
done

echo ""
echo "=== Tráfego concluído! Verifique o Dashboard no Grafana e o Service Map/Traces no New Relic. ==="
