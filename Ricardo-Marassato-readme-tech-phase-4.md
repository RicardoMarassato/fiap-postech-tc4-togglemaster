# Relatório de Entrega - Tech Challenge Fase 4 (ToggleMaster)

## 1. Identificação

* **Aluno:** Ricardo Marassato Campos
* **RM:** 370358
* **Username Discord:** `marassato7700`
* **Link do Repositório de Código:** [https://github.com/RicardoMarassato/fiap-postech-tc4-togglemaster](https://github.com/RicardoMarassato/fiap-postech-tc4-togglemaster)
* **Link do Vídeo de Demonstração:** *(Inserir link do vídeo no Google Drive / YouTube)*

---

## 2. Justificativa Técnica da Arquitetura

### 2.1. Arquitetura do OpenTelemetry (OTel Collector) Implementada
Para eliminar o acoplamento entre o código-fonte dos microsserviços e fornecedores específicos de monitoramento (*vendor lock-in*), implementamos o **OpenTelemetry Collector (`otel/opentelemetry-collector-contrib`)** no namespace `monitoring` como um **Hub Central de Telemetria (Deployment Gateway)**:

1. **Padronização da Instrumentação (OTLP):**
   Os 5 microsserviços (`auth-service` e `evaluation-service` em Go; `flag-service`, `targeting-service` e `analytics-service` em Python) utilizam exclusivamente o SDK oficial do OpenTelemetry e exportam **Traces, Métricas e Logs** via protocolo aberto **OTLP/HTTP** para o serviço interno `http://otel-collector.monitoring.svc.cluster.local:4318`.
2. **Propagação de Contexto Fim a Fim (W3C TraceContext + SQS):**
   Quando o `evaluation-service` recebe uma requisição em `GET /evaluate`, ele propaga o header `traceparent` (padrão W3C) nas chamadas HTTP concorrentes para o `flag-service` e `targeting-service`, que por sua vez propagam o mesmo contexto ao validar a chave de API no `auth-service`. Além disso, o `evaluation-service` injeta o `traceparent` no payload publicado na fila **Amazon SQS**, permitindo que o worker assíncrono do `analytics-service` extraia o contexto e registre o span de gravação no **Amazon DynamoDB** dentro do mesmo Trace Distribuído.
3. **Pipeline de Processamento e Roteamento Multi-Backend no OTel Collector:**
   * **Processors (`memory_limiter`, `resource`, `batch`):** Garantem proteção contra picos de memória nos worker nodes do EKS, enriquecem todos os sinais com metadados padronizados (`service.namespace=togglemaster`, `deployment.environment=production`) e agrupam os pacotes em lotes otimizados.
   * **Roteamento de Métricas -> Prometheus:** O OTel Collector exporta as métricas tanto via *Prometheus Remote Write* (`prometheusremotewrite`) quanto via endpoint de scrape (`:8889/metrics`), alimentando o Prometheus no cluster.
   * **Roteamento de Logs -> Grafana Loki:** Os logs estruturados em JSON (contendo `trace_id` e `span_id`) são enviados ao Loki via `loki` exporter, complementados pelo `Promtail` que coleta logs de todos os contêineres do cluster.
   * **Roteamento de Traces, Métricas e Logs -> New Relic APM:** O exportador `otlphttp/newrelic` encaminha a telemetria de alta cardinalidade via HTTPS para o endpoint OTLP nativo do New Relic (`https://otlp.nr-data.net:4318`).

---

### 2.2. Escolha de APM: New Relic vs Datadog
Optamos pelo **New Relic** pelos seguintes motivos técnicos e arquiteturais:
* **Suporte Nativo ao Protocolo OTLP:** O New Relic possui ingestão nativa OTLP (`otlp.nr-data.net:4318`), permitindo que o OTel Collector envie traces, métricas e logs diretamente usando apenas o exportador padrão `otlphttp` e o header `api-key`, sem exigir agentes proprietários pesados nos nós do Kubernetes nem exportadores específicos de fornecedor.
* **Correlação Automática de Service Map e Distributed Tracing:** A partir dos atributos semânticos do OpenTelemetry (`service.name`, `peer.service`, `db.system`, `messaging.system`), o New Relic constrói automaticamente o **Service Map** completo dos 5 microsserviços, bancos PostgreSQL, Redis, SQS e DynamoDB.
* **Modelo de Conta Educacional / Free Tier:** Oferece 100 GB/mês de ingestão gratuita com acesso integral ao Distributed Tracing, Service Map e Logs in Context sem restrição de funcionalidades críticas para estudantes.

---

### 2.3. Escolha de Gerenciamento de Incidentes: PagerDuty vs OpsGenie
Optamos pelo **PagerDuty** em conjunto com o **Discord (ChatOps)** pelos seguintes motivos:
* **Consolidação de Mercado e Descontinuação do OpsGenie:** A Atlassian anunciou oficialmente a descontinuação gradual do OpsGenie (migrando seus recursos para o Jira Service Management), enquanto o **PagerDuty** permanece como a plataforma líder de mercado dedicada à resposta a incidentes de missão crítica.
* **Events API v2 e Deduplicação Inteligente contra Alert Fatigue:** A integração nativa do Grafana com a Events API v2 do PagerDuty (`integrationKey`) realiza agrupamento automático por `dedup_key` / `group_by` (`alertname`, `service`), evitando que múltiplos disparos do mesmo incidente acordem o engenheiro de plantão repetidamente (*Alert Fatigue*) e fechando o incidente automaticamente (`auto-resolve`) assim que o **Self-Healing** restaura a saúde do serviço.

---

### 2.4. Estratégia de Self-Healing Automático
Para garantir mitigação imediata sem intervenção humana quando o alerta `AuthServiceHigh5xxErrorRate` (taxa de erros HTTP 5xx > 5%) dispara no Grafana:
1. **Automação In-Cluster (`self-healing-service`):** Um serviço dedicado rodando no namespace `monitoring` com `ServiceAccount` e `RoleBinding` restrito ao princípio de menor privilégio (permissão de `patch` apenas no recurso `deployments` do namespace `togglemaster`) recebe o webhook do Grafana e executa imediatamente o *Rollout Restart* (`kubectl.kubernetes.io/restartedAt`) no deployment afetado, com janela de *cooldown* para evitar loops de reinicialização.
2. **Automação via GitHub Actions (`self-healing.yml`):** Complementarmente, o evento aciona o workflow de Runbook Automation no GitHub Actions via `repository_dispatch`, registrando trilha de auditoria completa e notificando o canal do **Discord**.

---

## 3. Evidências Visuais Obrigatórias

> *(Adicione abaixo os 4 prints capturados durante a demonstração antes de exportar para PDF)*

### 3.1. Print do Dashboard Customizado do Grafana
*(Inserir captura de tela do dashboard `ToggleMaster - Observabilidade Total & Self-Healing (Fase 4)` exibindo uso de recursos do cluster, taxa de requisições RPS, taxa de erros 5xx e painel de logs do Loki em tempo real)*

### 3.2. Print de um Trace Distribuído e Service Map no New Relic (APM)
*(Inserir captura de tela do New Relic mostrando o Service Map com os 5 microsserviços e o Trace Distribuído de uma requisição `GET /evaluate` passando por `evaluation-service`, `flag-service`, `targeting-service`, `auth-service` e `analytics-service`)*

### 3.3. Print do Incidente no PagerDuty e Notificação no ChatOps (Discord)
*(Inserir captura de tela do incidente aberto automaticamente no PagerDuty e das mensagens de alerta + confirmação de Self-Healing no canal do Discord)*

### 3.4. Print do Log e Execução da Automação de Self-Healing
*(Inserir captura de tela do terminal mostrando o alerta `Firing`, o log do `self-healing-service` executando `kubectl rollout restart deployment/auth-service -n togglemaster` e o pod sendo recriado automaticamente no Kubernetes)*
