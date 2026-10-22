# GitOps - ArgoCD, Aplicações e Stack de Observabilidade (Fase 4)

Esta pasta contém todos os manifestos Kubernetes e definições do **ArgoCD** que gerenciam o estado desejado do cluster EKS:

## Estrutura de Diretórios

```text
gitops/
├── argocd/
│   ├── applications.yaml               # Define as 9 Applications do ArgoCD (Apps + Helm Charts Prometheus/Grafana/Loki + OTel)
│   └── install.sh                      # Script de bootstrap do ArgoCD e aplicação da stack completa
├── base/
│   ├── namespace.yaml                  # Namespace togglemaster
│   ├── configmap.yaml                  # URLs de serviços, SQS e DynamoDB
│   ├── secrets.yaml                    # Credenciais de RDS, Redis e API Keys
│   └── ingress.yaml                    # NGINX Ingress para os 5 microsserviços
├── apps/
│   ├── auth-service/deployment.yaml       # Deployment + Service com variáveis OTel
│   ├── flag-service/deployment.yaml       # Deployment + Service com variáveis OTel
│   ├── targeting-service/deployment.yaml  # Deployment + Service com variáveis OTel
│   ├── evaluation-service/deployment.yaml # Deployment + Service com variáveis OTel
│   └── analytics-service/deployment.yaml  # Deployment + Service com variáveis OTel
└── observability/
    ├── namespace.yaml                  # Namespace monitoring
    ├── secrets.yaml                    # Secrets para New Relic, PagerDuty e Discord
    ├── otel-collector.yaml             # OpenTelemetry Collector (Receivers OTLP -> Prometheus, Loki e New Relic)
    ├── grafana-dashboard.yaml          # ConfigMap com o Dashboard customizado do Grafana
    ├── grafana-alerting.yaml           # Regras de Alerta (>5% 5xx) + Contact Points (PagerDuty, Discord, Webhook)
    ├── self-healing.yaml               # Microsserviço/Webhook de Self-Healing + RBAC para Rollout Restart automático
    └── dashboards/
        └── togglemaster-dashboard.json # Arquivo JSON do Dashboard customizado do Grafana
```
