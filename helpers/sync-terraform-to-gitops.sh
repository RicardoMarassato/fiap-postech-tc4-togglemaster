#!/bin/bash
# =============================================================================
# Sincroniza automaticamente os outputs do Terraform (RDS, Redis, SQS, EKS)
# para os manifestos GitOps (gitops/base/secrets.yaml e configmap.yaml)
# =============================================================================
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
TF_DIR="${ROOT_DIR}/terraform"
REGION="us-east-1"

echo "=== 1. Configurando kubectl para o cluster EKS ==="
CLUSTER_NAME=$(terraform -chdir="${TF_DIR}" output -raw eks_cluster_name)
aws eks update-kubeconfig --region "${REGION}" --name "${CLUSTER_NAME}"

echo "=== 2. Extraindo endpoints de RDS, Redis e SQS do Terraform & Secrets Manager ==="
REDIS_URL=$(terraform -chdir="${TF_DIR}" output -raw redis_url)
SQS_URL=$(terraform -chdir="${TF_DIR}" output -raw sqs_queue_url)
DYNAMO_TABLE=$(terraform -chdir="${TF_DIR}" output -raw dynamodb_table_name)

build_pg_url() {
  local SECRET_NAME="$1"
  local SECRET_JSON
  SECRET_JSON=$(aws secretsmanager get-secret-value --region "${REGION}" --secret-id "${SECRET_NAME}" --query SecretString --output text)
  python3 -c '
import json, sys, urllib.parse
data = json.loads(sys.argv[1])
user = urllib.parse.quote(str(data["username"]), safe="")
pwd = urllib.parse.quote(str(data["password"]), safe="")
host = data["host"]
port = data.get("port", 5432)
db = data["database"]
print(f"postgresql://{user}:{pwd}@{host}:{port}/{db}")
' "${SECRET_JSON}"
}

AUTH_DB_URL=$(build_pg_url "togglemaster-prod/auth-db-credentials")
FLAGS_DB_URL=$(build_pg_url "togglemaster-prod/flags-db-credentials")
TARGETING_DB_URL=$(build_pg_url "togglemaster-prod/targeting-db-credentials")

echo "=== 3. Atualizando gitops/base/secrets.yaml e gitops/base/configmap.yaml ==="
cat > "${ROOT_DIR}/gitops/base/secrets.yaml" <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: auth-db-credentials
  namespace: togglemaster
type: Opaque
stringData:
  url: "${AUTH_DB_URL}"
---
apiVersion: v1
kind: Secret
metadata:
  name: flags-db-credentials
  namespace: togglemaster
type: Opaque
stringData:
  url: "${FLAGS_DB_URL}"
---
apiVersion: v1
kind: Secret
metadata:
  name: targeting-db-credentials
  namespace: togglemaster
type: Opaque
stringData:
  url: "${TARGETING_DB_URL}"
---
apiVersion: v1
kind: Secret
metadata:
  name: redis-credentials
  namespace: togglemaster
type: Opaque
stringData:
  url: "${REDIS_URL}"
---
apiVersion: v1
kind: Secret
metadata:
  name: auth-service-secrets
  namespace: togglemaster
type: Opaque
stringData:
  master-key: "admin-secreto-123"
---
apiVersion: v1
kind: Secret
metadata:
  name: evaluation-service-secrets
  namespace: togglemaster
type: Opaque
stringData:
  api-key: "tm_key_production_secret"
EOF

cat > "${ROOT_DIR}/gitops/base/configmap.yaml" <<EOF
# =============================================================================
# ConfigMaps - Configurações não sensíveis
# =============================================================================
apiVersion: v1
kind: ConfigMap
metadata:
  name: aws-config
  namespace: togglemaster
data:
  sqs-url: "${SQS_URL}"
  dynamodb-table: "${DYNAMO_TABLE}"
  aws-region: "${REGION}"
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: service-urls
  namespace: togglemaster
data:
  auth-service-url: "http://auth-service:8001"
  flag-service-url: "http://flag-service:8002"
  targeting-service-url: "http://targeting-service:8003"
  evaluation-service-url: "http://evaluation-service:8004"
  analytics-service-url: "http://analytics-service:8005"
EOF

kubectl create namespace togglemaster --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -f "${ROOT_DIR}/gitops/base/secrets.yaml"
kubectl apply -f "${ROOT_DIR}/gitops/base/configmap.yaml"

echo "=== Sucesso! Manifestos GitOps atualizados e aplicados no cluster! ==="
