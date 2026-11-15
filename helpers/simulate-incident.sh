#!/bin/bash
# =============================================================================
# Simulador de Incidente e Prova Real de Self-Healing (Fase 4)
# =============================================================================
# Este script:
# 1. Ativa o Chaos Mode em memória no pod do auth-service (ou evaluation-service),
#    fazendo com que ele passe a retornar HTTP 500 Internal Server Error.
# 2. Gera tráfego contínuo para elevar a taxa de erros HTTP 5xx acima de 5%.
# 3. O alerta AuthServiceHigh5xxErrorRate dispara (Firing) no Grafana:
#    - Abre incidente automaticamente no PagerDuty
#    - Envia notificação detalhada no Discord (ChatOps)
#    - Aciona o Webhook do self-healing-service no cluster
# 4. O self-healing-service executa automaticamente o Rollout Restart do
#    deployment/auth-service, criando um novo pod saudável (sem Chaos Mode)
#    e mitigando o incidente sem intervenção humana!
# =============================================================================
set -e

SERVICE="${1:-auth-service}"
NAMESPACE="togglemaster"

if [ "${SERVICE}" == "auth-service" ]; then
  PORT=8001
  LOCAL_PORT=18001
  TEST_PATH="/validate"
else
  PORT=8004
  LOCAL_PORT=18004
  TEST_PATH="/evaluate?user_id=chaos-user&flag_name=checkout-v2"
fi

echo "============================================================================="
echo "🔥 INICIANDO PROVA REAL DE INCIDENTE E SELF-HEALING (${SERVICE})"
echo "============================================================================="

echo "1. Pods atuais de ${SERVICE} antes do incidente:"
kubectl get pods -n "${NAMESPACE}" -l "app=${SERVICE}"

kubectl port-forward "svc/${SERVICE}" -n "${NAMESPACE}" "${LOCAL_PORT}:${PORT}" >/dev/null 2>&1 &
PF_PID=$!
cleanup() {
  kill ${PF_PID} 2>/dev/null || true
}
trap cleanup EXIT
sleep 2

echo ""
echo "2. Injetando falha crítica (Chaos Mode -> HTTP 500) em ${SERVICE}..."
CHAOS_OK=false
for attempt in $(seq 1 15); do
  CHAOS_RESP=$(curl -s -X POST "http://localhost:${LOCAL_PORT}/chaos?enable=true" || true)
  if echo "${CHAOS_RESP}" | grep -q "true"; then
    echo "   Chaos Mode ativado com sucesso: ${CHAOS_RESP}"
    CHAOS_OK=true
    break
  fi
  sleep 1
done

if [ "${CHAOS_OK}" != "true" ]; then
  echo "   Aviso: tentando ativar Chaos Mode via kubectl exec/port-forward..."
  kubectl port-forward "svc/${SERVICE}" -n "${NAMESPACE}" "${LOCAL_PORT}:${PORT}" >/dev/null 2>&1 &
  PF_PID=$!
  sleep 4
  curl -s -X POST "http://localhost:${LOCAL_PORT}/chaos?enable=true" || true
  echo ""
fi

echo ""
echo "3. Gerando requisições com erro HTTP 500 para disparar o alerta (>5% 5xx)..."
echo ""

for i in $(seq 1 60); do
  # Reconecta port-forward caso o pod tenha sido reiniciado pelo Self-Healing!
  if ! kill -0 ${PF_PID} 2>/dev/null; then
    kubectl port-forward "svc/${SERVICE}" -n "${NAMESPACE}" "${LOCAL_PORT}:${PORT}" >/dev/null 2>&1 &
    PF_PID=$!
    sleep 3
  fi

  HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" "http://localhost:${LOCAL_PORT}${TEST_PATH}" || echo "restarting")
  # Dispara 3 chamadas rápidas por iteração para acelerar a subida da métrica no Prometheus
  curl -s -o /dev/null "http://localhost:${LOCAL_PORT}${TEST_PATH}" 2>/dev/null || true
  curl -s -o /dev/null "http://localhost:${LOCAL_PORT}${TEST_PATH}" 2>/dev/null || true
  echo "   [Req ${i}/60] GET ${TEST_PATH} -> HTTP ${HTTP_CODE}"

  if [ "${HTTP_CODE}" == "restarting" ] || [ "${HTTP_CODE}" == "000" ]; then
    echo "   ⚡ Conexão interrompida porque o Self-Healing reiniciou o pod! Reconectando ao novo pod..."
    kill ${PF_PID} 2>/dev/null || true
    sleep 4
    kubectl port-forward "svc/${SERVICE}" -n "${NAMESPACE}" "${LOCAL_PORT}:${PORT}" >/dev/null 2>&1 &
    PF_PID=$!
    sleep 3
  fi
  sleep 1
done

echo ""
echo "4. Verificando logs da automação de Self-Healing no cluster:"
kubectl logs -n monitoring -l app=self-healing-service --tail=20 || true

echo ""
echo "5. Estado final dos pods de ${SERVICE} após o Self-Healing:"
kubectl get pods -n "${NAMESPACE}" -l "app=${SERVICE}"
