#!/bin/bash
# =============================================================================
# Script para instalar ArgoCD e a Stack de Observabilidade no cluster EKS
# =============================================================================

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "=== Instalando ArgoCD ==="

# Criar namespaces base
kubectl create namespace argocd --dry-run=client -o yaml | kubectl apply -f -
kubectl create namespace monitoring --dry-run=client -o yaml | kubectl apply -f -
kubectl create namespace togglemaster --dry-run=client -o yaml | kubectl apply -f -

# Instalar ArgoCD usando Server-Side Apply (necessário para CRDs grandes)
kubectl apply -n argocd --server-side --force-conflicts -f https://raw.githubusercontent.com/argoproj/argo-cd/stable/manifests/install.yaml

# Desativar componentes opcionais do ArgoCD para economizar pods e memória no cluster
kubectl scale deployment argocd-dex-server argocd-notifications-controller argocd-applicationset-controller -n argocd --replicas=0 2>/dev/null || true

echo "Aguardando pods do ArgoCD ficarem prontos..."
kubectl wait --for=condition=available --timeout=300s deployment/argocd-server -n argocd

# Aplicar as Applications do ToggleMaster (5 microsserviços + Prometheus + Loki + Grafana + OTel Collector + Self-Healing)
echo "Aplicando Applications GitOps (ToggleMaster + Observabilidade + Self-Healing)..."
kubectl apply -f "${SCRIPT_DIR}/applications.yaml"

echo ""
echo "=== ArgoCD e Applications configurados com sucesso! ==="
echo ""
echo "Para obter a senha inicial do admin do ArgoCD:"
echo "  kubectl -n argocd get secret argocd-initial-admin-secret -o jsonpath='{.data.password}' | base64 -d && echo"
echo ""
echo "Para acessar a UI do ArgoCD (port-forward):"
echo "  kubectl port-forward svc/argocd-server -n argocd 8080:443"
echo ""
echo "Para acessar o Grafana (usuário: admin / senha: admin):"
echo "  kubectl port-forward svc/kube-prometheus-stack-grafana -n monitoring 3000:80"
echo ""
echo "=== Setup completo! ==="
