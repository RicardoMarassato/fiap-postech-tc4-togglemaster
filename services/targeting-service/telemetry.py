import json
import logging
import os
import threading
import time
from flask import Response, jsonify, request
from opentelemetry import metrics, trace
from opentelemetry.exporter.otlp.proto.http._log_exporter import OTLPLogExporter
from opentelemetry.exporter.otlp.proto.http.metric_exporter import OTLPMetricExporter
from opentelemetry.exporter.otlp.proto.http.trace_exporter import OTLPSpanExporter
from opentelemetry.instrumentation.flask import FlaskInstrumentor
from opentelemetry.instrumentation.logging import LoggingInstrumentor
from opentelemetry.instrumentation.psycopg2 import Psycopg2Instrumentor
from opentelemetry.instrumentation.requests import RequestsInstrumentor
from opentelemetry.sdk._logs import LoggerProvider, LoggingHandler
from opentelemetry.sdk._logs.export import BatchLogRecordProcessor
from opentelemetry.sdk.metrics import MeterProvider
from opentelemetry.sdk.metrics.export import PeriodicExportingMetricReader
from opentelemetry.sdk.resources import Resource
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import BatchSpanProcessor

_metrics_lock = threading.Lock()
_prom_req_counts = {}
_prom_dur_sums = {}
_prom_dur_counts = {}
_chaos_enabled = False


class JsonTraceFormatter(logging.Formatter):
    """Formata logs em JSON estruturado com trace_id e span_id para o Loki."""

    def __init__(self, service_name: str):
        super().__init__()
        self.service_name = service_name

    def format(self, record: logging.LogRecord) -> str:
        span = trace.get_current_span()
        span_ctx = span.get_span_context() if span else None
        payload = {
            "timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(record.created)),
            "level": record.levelname,
            "service": self.service_name,
            "message": record.getMessage(),
        }
        if span_ctx and span_ctx.is_valid:
            payload["trace_id"] = format(span_ctx.trace_id, "032x")
            payload["span_id"] = format(span_ctx.span_id, "016x")
        return json.dumps(payload)


def setup_telemetry(app, service_name: str):
    """Inicializa OpenTelemetry (Traces, Metrics e Logs) e instrumenta Flask, Requests e Psycopg2."""
    otlp_endpoint = os.getenv(
        "OTEL_EXPORTER_OTLP_ENDPOINT",
        "http://otel-collector.monitoring.svc.cluster.local:4318",
    ).rstrip("/")

    resource = Resource.create(
        {
            "service.name": service_name,
            "service.version": "1.0.0",
            "service.namespace": "togglemaster",
            "deployment.environment": os.getenv("ENVIRONMENT", "production"),
        }
    )

    # 1. Traces
    tracer_provider = TracerProvider(resource=resource)
    span_exporter = OTLPSpanExporter(endpoint=f"{otlp_endpoint}/v1/traces")
    tracer_provider.add_span_processor(BatchSpanProcessor(span_exporter))
    trace.set_tracer_provider(tracer_provider)

    # 2. Metrics
    metric_exporter = OTLPMetricExporter(endpoint=f"{otlp_endpoint}/v1/metrics")
    metric_reader = PeriodicExportingMetricReader(
        metric_exporter, export_interval_millis=10000
    )
    meter_provider = MeterProvider(resource=resource, metric_readers=[metric_reader])
    metrics.set_meter_provider(meter_provider)
    meter = metrics.get_meter(service_name)

    http_requests_counter = meter.create_counter(
        name="http_requests_total",
        description="Total de requisicoes HTTP processadas",
    )
    http_duration_hist = meter.create_histogram(
        name="http_request_duration_seconds",
        description="Duracao das requisicoes HTTP em segundos",
        unit="s",
    )

    # 3. Logs via OTLP + JSON estruturado no stdout para o Loki
    try:
        logger_provider = LoggerProvider(resource=resource)
        log_exporter = OTLPLogExporter(endpoint=f"{otlp_endpoint}/v1/logs")
        logger_provider.add_log_record_processor(BatchLogRecordProcessor(log_exporter))
        otel_log_handler = LoggingHandler(
            level=logging.INFO, logger_provider=logger_provider
        )
        root_logger = logging.getLogger()
        root_logger.addHandler(otel_log_handler)
        for handler in root_logger.handlers:
            if not isinstance(handler, LoggingHandler):
                handler.setFormatter(JsonTraceFormatter(service_name))
    except Exception as exc:  # pragma: no cover
        logging.getLogger(__name__).warning("Aviso ao configurar OTLP Log Exporter: %s", exc)

    # 4. Auto-instrumentação de bibliotecas
    LoggingInstrumentor().instrument(set_logging_format=False)
    RequestsInstrumentor().instrument()
    Psycopg2Instrumentor().instrument()
    FlaskInstrumentor().instrument_app(app, excluded_urls="/health,/metrics")

    @app.before_request
    def _before_request():
        request._start_time = time.time()
        if _chaos_enabled and request.path not in ("/health", "/metrics", "/chaos"):
            span = trace.get_current_span()
            if span:
                span.set_attribute("error", True)
            logging.getLogger(service_name).error(
                "Falha critica simulada (Chaos Mode ativo) em %s", service_name
            )
            return (
                jsonify({"error": f"Erro interno crítico simulado em {service_name}"}),
                500,
            )
        return None

    @app.after_request
    def _after_request(response):
        if request.path == "/metrics":
            return response
        duration = time.time() - getattr(request, "_start_time", time.time())
        route = request.url_rule.rule if request.url_rule else request.path
        status_str = str(response.status_code)

        attrs = {
            "service": service_name,
            "method": request.method,
            "route": route,
            "status_code": status_str,
        }
        http_requests_counter.add(1, attrs)
        http_duration_hist.record(duration, attrs)

        key = f'service="{service_name}",method="{request.method}",route="{route}",status_code="{status_str}"'
        with _metrics_lock:
            _prom_req_counts[key] = _prom_req_counts.get(key, 0) + 1
            _prom_dur_sums[key] = _prom_dur_sums.get(key, 0.0) + duration
            _prom_dur_counts[key] = _prom_dur_counts.get(key, 0) + 1

        return response

    @app.route("/metrics")
    def _metrics_endpoint():
        lines = [
            "# HELP http_requests_total Total de requisicoes HTTP processadas",
            "# TYPE http_requests_total counter",
        ]
        with _metrics_lock:
            for labels, count in _prom_req_counts.items():
                lines.append(f"http_requests_total{{{labels}}} {count}")
            lines.append(
                "# HELP http_request_duration_seconds_sum Soma da duracao das requisicoes HTTP"
            )
            lines.append("# TYPE http_request_duration_seconds_sum counter")
            for labels, total in _prom_dur_sums.items():
                lines.append(f"http_request_duration_seconds_sum{{{labels}}} {total:.6f}")
            lines.append(
                "# HELP http_request_duration_seconds_count Contagem de requisicoes HTTP medidas"
            )
            lines.append("# TYPE http_request_duration_seconds_count counter")
            for labels, count in _prom_dur_counts.items():
                lines.append(f"http_request_duration_seconds_count{{{labels}}} {count}")
        return Response("\n".join(lines) + "\n", mimetype="text/plain; version=0.0.4")

    @app.route("/chaos", methods=["GET", "POST"])
    def _chaos_endpoint():
        global _chaos_enabled
        enable_arg = request.args.get("enable", "").lower()
        if enable_arg in ("true", "1"):
            _chaos_enabled = True
            logging.getLogger(service_name).error(
                "CHAOS MODE ATIVADO em %s: retornando HTTP 500", service_name
            )
        elif enable_arg in ("false", "0"):
            _chaos_enabled = False
            logging.getLogger(service_name).info(
                "CHAOS MODE DESATIVADO em %s", service_name
            )
        return jsonify({"service": service_name, "chaos_enabled": _chaos_enabled}), 200
