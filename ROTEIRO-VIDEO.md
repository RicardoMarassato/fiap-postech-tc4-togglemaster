# Roteiro de Gravação — Tech Challenge Fase 4 (ToggleMaster)

**Tempo estimado:** 10 a 15 minutos (limite da FIAP: 25 min)
**Dica:** Fale com suas próprias palavras, como se estivesse apresentando para um colega de equipe!

---

## 0. Checklist Rápido Antes de Apertar REC

- **Terminal (`tmux`):**
  - Esquerda: `watch` dos nodes e pods (`togglemaster` e `monitoring`)
  - Direita topo: `kubectl logs -n monitoring deployment/self-healing-service -f`
  - Direita baixo: pronto na pasta do projeto para rodar `./helpers/generate-traffic.sh` e `./helpers/simulate-incident.sh`
- **Abas do Navegador Abertas:**
  1. **ArgoCD:** `https://localhost:8080`
  2. **Grafana Dashboard:** `http://localhost:3000/d/togglemaster-phase4-overview`
  3. **New Relic (Traces / APM):** `https://one.newrelic.com` (com `Multi-span only` ativado em *Traces*)
  4. **OpsGenie / Jira Service Management:** menu *Operações -> Alertas*
  5. **Discord:** canal onde o Webhook está configurado
- **VS Code Aberto** com 2 arquivos no jeito:
  - `gitops/observability/otel-collector.yaml`
  - `services/evaluation-service/sqs.go`

---

## Bloco 1: Apresentação, EKS e GitOps com ArgoCD (~2 min)

1. **Apresentação (no Terminal `tmux`):**
   - *"Fala pessoal, sou o Ricardo Marassato (RM 370358), e vou demonstrar o projeto da Fase 4 do Tech Challenge na plataforma ToggleMaster, focado em Observabilidade Total com OpenTelemetry e Self-Healing."*
2. **Mostrar os Nodes e Pods no `tmux`:**
   - Mostre na esquerda os 2 worker nodes `Ready` no EKS (`m7i-flex.large` com prefix delegation ativado na VPC CNI por conta do limite de 4 vCPUs da conta AWS).
   - Mostre os 5 microsserviços rodando no namespace `togglemaster` (`auth`, `flag`, `targeting`, `evaluation`, `analytics`).
   - Mostre a stack de observabilidade no namespace `monitoring` (`Prometheus`, `Grafana`, `Loki`, `Promtail`, `OTel Collector` e o `self-healing-service`).
3. **Mostrar a Aba 1 (ArgoCD):**
   - Mostre que toda a aplicação e a stack de observabilidade foram provisionadas via GitOps pelo ArgoCD sincronizando direto do repositório no GitHub, com todos os cards `Healthy` e `Synced`.

---

## Bloco 2: OpenTelemetry Collector e Código Instrumentado (~2 a 3 min)

1. **No VS Code — Mostrar `gitops/observability/otel-collector.yaml`:**
   - Explique por que usamos o **OpenTelemetry Collector** como peça central (evitar *vendor lock-in*):
     - **Receivers (`otlp`):** Recebe Traces, Métricas e Logs dos 5 microsserviços (Go e Python) via protocolo aberto OTLP (`4317`/`4318`), sem nenhum agente proprietário instalado no código das aplicações.
     - **Processors (`memory_limiter`, `resource`, `batch`):** Protegem a memória do cluster, adicionam labels padronizadas (`namespace=togglemaster`) e enviam em lote.
     - **Exporters / Pipelines:** Roteiam os dados simultaneamente para múltiplos destinos:
       - **Métricas** -> `Prometheus` (no cluster) e `New Relic`
       - **Logs** -> `Grafana Loki` (no cluster) e `New Relic`
       - **Traces** -> `New Relic APM` via endpoint OTLP nativo (`otlp.nr-data.net`).
2. **No VS Code — Mostrar `services/evaluation-service/sqs.go` (linhas 18 a 46):**
   - Explique em 2 frases a propagação de contexto:
     - *"Para que uma requisição tenha o mesmo `trace_id` passando pelos 5 serviços, o `evaluation-service` propaga o cabeçalho W3C `traceparent` tanto nas chamadas HTTP para o `flag-service` e `targeting-service` (que validam o token no `auth-service`) quanto dentro da mensagem enviada para a fila **SQS**, para que o `analytics-service` continue o mesmo trace ao gravar no DynamoDB."*

---

## Bloco 3: Grafana, Prometheus e Loki (~2 a 3 min)

1. **No Terminal (`tmux` direita baixo) — Gerar tráfego (opcional se já gerou antes):**
   ```bash
   ./helpers/generate-traffic.sh 15
   ```
2. **Mostrar a Aba 2 (Grafana Dashboard):**
   - Mostre a **Linha 1 (Saúde do Cluster):** Uso de CPU, Memória e Pods ativos por microsserviço no namespace `togglemaster`.
   - Mostre a **Linha 2 (Métricas de Aplicação / Golden Signals):** Taxa de Requisições (RPS), Taxa de Erros HTTP 5xx (%) e Latência P95.
   - Mostre a **Linha 3 (Logs em Tempo Real do Loki):** Mostre que os logs estão chegando em formato JSON estruturado contendo `trace_id` e `span_id`, correlacionados no mesmo dashboard.
   - 📸 **[TIRAR PRINT 1]** — Print do Dashboard do Grafana populado!

---

## Bloco 4: APM Externo — New Relic (Service Map e Distributed Tracing) (~2 min)

1. **Mostrar a Aba 3 (New Relic):**
   - Mostre os 5 serviços instrumentados com OpenTelemetry.
   - Na tela de **Traces** (com `Multi-span only` ativado), clique no grupo **`GET /evaluate`** e abra um Trace.
   - Mostre a cascata (*waterfall*) de spans: a requisição entra no `evaluation-service`, chama `flag-service` e `targeting-service` em paralelo, ambos validam a API Key no `auth-service` (consultando o RDS PostgreSQL), e por fim publica no **SQS** onde o `analytics-service` consome e faz o `PutItem` no **DynamoDB**.
   - Mostre também o **Service Map** (mapa de dependências entre os serviços).
   - 📸 **[TIRAR PRINT 2]** — Print do Trace Distribuído / Service Map no New Relic!

---

## Bloco 5: Simulação de Incidente, Alerting (OpsGenie + Discord) e Self-Healing (~4 min)

1. **No Grafana -> *Alerting -> Alert rules*:**
   - Mostre rapidamente a regra `AuthServiceHigh5xxErrorRate` (configurada para disparar se a taxa de erros HTTP 5xx passar de **5%**) e comente que escolhemos o **OpsGenie (integrado ao Jira Service Management)** em conjunto com o **Discord (ChatOps)** porque o PagerDuty bloqueia domínios de e-mail pessoais/acadêmicos e o OpsGenie já faz deduplicação nativa de alertas.
2. **No Terminal (`tmux` direita baixo) — Disparar a simulação de incidente:**
   ```bash
   ./helpers/simulate-incident.sh auth-service
   ```
   - Explique o que está acontecendo ao vivo:
     - O script ativou a falha no `auth-service`, gerando respostas HTTP 500 e elevando a taxa de erro acima de 5%.
3. **Acompanhar o Alerta e o Self-Healing ao vivo:**
   - **No Terminal (`tmux`):** Mostre na direita em cima o `self-healing-service` recebendo o webhook do Grafana (`firing`) e executando automaticamente o `Rollout Restart` no `deployment/auth-service` via API do Kubernetes — e na esquerda do meio o pod novo nascendo e o antigo dando `Terminating`!
   - 📸 **[TIRAR PRINT 4]** — Print da janela do `tmux` mostrando o log do `self-healing-service` + o pod novo do `auth-service`!
   - **Na Aba 4 (OpsGenie / Jira Service Management) e Aba 5 (Discord):**
     - Mostre o alerta crítico aberto automaticamente no **OpsGenie / JSM** (*Operações -> Alertas*).
     - Mostre no **Discord** as notificações automáticas do incidente e da ação de Self-Healing concluída.
   - 📸 **[TIRAR PRINT 3]** — Print do alerta no OpsGenie/JSM e das mensagens no Discord!
   - **Voltar ao Dashboard do Grafana:** Mostre que após o restart automático do pod, a taxa de erros HTTP 5xx caiu de volta para **0%** sem nenhuma intervenção humana!

---

## Pós-Gravação (Não esquecer!)

Assim que parar a gravação e salvar os 4 prints, rode para economizar seus créditos AWS:

```bash
terraform -chdir=terraform destroy -auto-approve
```
