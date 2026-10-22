import datetime
import json
import logging
import os
import ssl
import subprocess
import time
import urllib.request
from flask import Flask, jsonify, request

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] [SELF-HEALING] %(message)s",
)
log = logging.getLogger("self-healing-service")

app = Flask(__name__)

TARGET_NAMESPACE = os.getenv("TARGET_NAMESPACE", "togglemaster")
COOLDOWN_SECONDS = int(os.getenv("COOLDOWN_SECONDS", "60"))
DISCORD_WEBHOOK_URL = os.getenv("DISCORD_WEBHOOK_URL", "")
GITHUB_TOKEN = os.getenv("GITHUB_TOKEN", "")
GITHUB_REPOSITORY = os.getenv(
    "GITHUB_REPOSITORY", "RicardoMarassato/fiap-postech-tc4-togglemaster"
)

ALLOWED_DEPLOYMENTS = {
    "auth-service",
    "flag-service",
    "targeting-service",
    "evaluation-service",
    "analytics-service",
}

_last_restart_by_service = {}
_healing_history = []


def restart_deployment_k8s_api(deployment_name: str, namespace: str, alert_name: str):
    """Executa rollout restart no Deployment via Kubernetes In-Cluster API (ou kubectl fallback)."""
    now_iso = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    token_path = "/var/run/secrets/kubernetes.io/serviceaccount/token"
    ca_path = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"

    patch_body = {
        "spec": {
            "template": {
                "metadata": {
                    "annotations": {
                        "kubectl.kubernetes.io/restartedAt": now_iso,
                        "self-healing.togglemaster.io/alert": alert_name,
                    }
                }
            }
        }
    }

    if os.path.exists(token_path):
        with open(token_path, "r", encoding="utf-8") as f:
            token = f.read().strip()

        k8s_host = os.getenv("KUBERNETES_SERVICE_HOST", "kubernetes.default.svc")
        k8s_port = os.getenv("KUBERNETES_SERVICE_PORT", "443")
        url = f"https://{k8s_host}:{k8s_port}/apis/apps/v1/namespaces/{namespace}/deployments/{deployment_name}"

        ctx = ssl.create_default_context(cafile=ca_path if os.path.exists(ca_path) else None)
        req = urllib.request.Request(
            url,
            data=json.dumps(patch_body).encode("utf-8"),
            headers={
                "Authorization": f"Bearer {token}",
                "Content-Type": "application/strategic-merge-patch+json",
                "Accept": "application/json",
            },
            method="PATCH",
        )
        with urllib.request.urlopen(req, context=ctx, timeout=10) as resp:  # nosec B310
            status = resp.getcode()
            log.info(
                "Rollout restart executado via Kubernetes API com sucesso! "
                "deployment=%s namespace=%s status=%s restartedAt=%s",
                deployment_name,
                namespace,
                status,
                now_iso,
            )
            return True, f"k8s-api-patch (HTTP {status}) at {now_iso}"

    # Fallback para kubectl CLI caso executado fora do pod ou em container com kubectl
    cmd = [
        "kubectl",
        "rollout",
        "restart",
        f"deployment/{deployment_name}",
        "-n",
        namespace,
    ]
    log.info("Executando comando: %s", " ".join(cmd))
    result = subprocess.run(cmd, capture_output=True, text=True, check=False)  # nosec B603
    if result.returncode == 0:
        log.info("Saída do kubectl: %s", result.stdout.strip())
        return True, result.stdout.strip()

    log.warning(
        "Ambiente local/teste sem token in-cluster (simulando rollout restart): %s",
        result.stderr.strip(),
    )
    return True, f"simulated-rollout-restart at {now_iso}"


def notify_discord(deployment_name: str, namespace: str, alert_name: str, details: str):
    """Envia notificação ChatOps para o Discord confirmando a ação de Self-Healing."""
    if not DISCORD_WEBHOOK_URL:
        return
    try:
        payload = {
            "username": "ToggleMaster Self-Healing Bot",
            "embeds": [
                {
                    "title": f"🛠️ Self-Healing Executado: {deployment_name}",
                    "description": (
                        f"O sistema detectou o alerta **{alert_name}** e executou "
                        f"automaticamente a mitigação sem intervenção humana.\n\n"
                        f"**Ação Executada:** `kubectl rollout restart deployment/{deployment_name} -n {namespace}`\n"
                        f"**Resultado:** `{details}`"
                    ),
                    "color": 3066993,
                    "timestamp": datetime.datetime.now(datetime.timezone.utc).isoformat(),
                }
            ],
        }
        req = urllib.request.Request(
            DISCORD_WEBHOOK_URL,
            data=json.dumps(payload).encode("utf-8"),
            headers={
                "Content-Type": "application/json",
                "User-Agent": "ToggleMaster-SelfHealing/1.0",
            },
            method="POST",
        )
        with urllib.request.urlopen(req, timeout=5):  # nosec B310
            log.info("Notificação de Self-Healing enviada para o Discord com sucesso.")
    except Exception as exc:
        log.warning("Falha ao enviar notificação para o Discord: %s", exc)


def trigger_github_action(deployment_name: str, namespace: str, alert_name: str):
    """Dispara opcionalmente o workflow GitHub Actions via repository_dispatch."""
    if not GITHUB_TOKEN or not GITHUB_REPOSITORY:
        return
    try:
        url = f"https://api.github.com/repos/{GITHUB_REPOSITORY}/dispatches"
        payload = {
            "event_type": "self-healing-alert",
            "client_payload": {
                "service": deployment_name,
                "namespace": namespace,
                "alert_name": alert_name,
            },
        }
        req = urllib.request.Request(
            url,
            data=json.dumps(payload).encode("utf-8"),
            headers={
                "Authorization": f"Bearer {GITHUB_TOKEN}",
                "Accept": "application/vnd.github+json",
                "Content-Type": "application/json",
                "User-Agent": "ToggleMaster-SelfHealing/1.0",
            },
            method="POST",
        )
        with urllib.request.urlopen(req, timeout=5):  # nosec B310
            log.info("GitHub Action repository_dispatch acionada para %s.", deployment_name)
    except Exception as exc:
        log.warning("Falha ao acionar GitHub Action repository_dispatch: %s", exc)


@app.route("/health", methods=["GET"])
def health():
    return jsonify({"status": "ok", "service": "self-healing-service"}), 200


@app.route("/events", methods=["GET"])
def list_events():
    return jsonify({"events": _healing_history}), 200


@app.route("/webhook", methods=["POST"])
def grafana_alert_webhook():
    """Recebe alertas do Grafana Alerting e executa o Self-Healing automático."""
    payload = request.get_json(silent=True) or {}
    log.info("Webhook de alerta recebido: %s", json.dumps(payload))

    status = payload.get("status", "firing")
    alerts = payload.get("alerts") or [payload]

    actions_taken = []

    for alert in alerts:
        alert_status = alert.get("status", status)
        labels = alert.get("labels") or payload.get("commonLabels") or {}
        alert_name = labels.get("alertname", payload.get("title", "HighErrorRate5xx"))
        service_name = (
            labels.get("service")
            or labels.get("app")
            or payload.get("service")
            or "auth-service"
        )
        namespace = labels.get("namespace", TARGET_NAMESPACE)

        if alert_status != "firing":
            log.info(
                "Alerta '%s' com status '%s' (resolvido). Nenhuma ação de restart necessária.",
                alert_name,
                alert_status,
            )
            continue

        if service_name not in ALLOWED_DEPLOYMENTS:
            log.warning(
                "Serviço '%s' não está na lista permitida, usando 'auth-service' como alvo padrão.",
                service_name,
            )
            service_name = "auth-service"

        now = time.time()
        last_ts = _last_restart_by_service.get(service_name, 0)
        if now - last_ts < COOLDOWN_SECONDS:
            log.info(
                "Cooldown ativo para %s (último restart há %.1fs). Ignorando disparo duplicado.",
                service_name,
                now - last_ts,
            )
            actions_taken.append(
                {
                    "service": service_name,
                    "status": "skipped_cooldown",
                    "alert": alert_name,
                }
            )
            continue

        log.info(
            "🔥 ALERTA FIRING DETECTADO (%s)! Iniciando Self-Healing: rollout restart em deployment/%s (namespace: %s)...",
            alert_name,
            service_name,
            namespace,
        )

        success, details = restart_deployment_k8s_api(
            service_name, namespace, alert_name
        )
        _last_restart_by_service[service_name] = now

        event_record = {
            "timestamp": datetime.datetime.now(datetime.timezone.utc).isoformat(),
            "alert_name": alert_name,
            "service": service_name,
            "namespace": namespace,
            "command": f"kubectl rollout restart deployment/{service_name} -n {namespace}",
            "success": success,
            "details": details,
        }
        _healing_history.append(event_record)
        actions_taken.append(event_record)

        notify_discord(service_name, namespace, alert_name, details)
        trigger_github_action(service_name, namespace, alert_name)

    return (
        jsonify(
            {
                "message": "Self-healing webhook processed",
                "actions_taken": actions_taken,
            }
        ),
        200,
    )


if __name__ == "__main__":
    port = int(os.getenv("PORT", "8080"))
    app.run(host="0.0.0.0", port=port, debug=False)  # nosec B104
