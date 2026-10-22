package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
	"go.opentelemetry.io/otel/trace"
)

var (
	tracer              trace.Tracer
	meter               metric.Meter
	httpRequestsCounter metric.Int64Counter
	httpDurationHist    metric.Float64Histogram

	// Métricas em memória para exposição no endpoint /metrics (compatibilidade Prometheus/OTel)
	metricsMu      sync.Mutex
	promReqCounts  = make(map[string]int64)
	promDurSums    = make(map[string]float64)
	promDurCounts  = make(map[string]int64)
)

// initTelemetry configura o OpenTelemetry (Traces e Metrics via OTLP HTTP) enviando para o OTel Collector
func initTelemetry(ctx context.Context, serviceName string) (func(context.Context) error, error) {
	otlpEndpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if otlpEndpoint == "" {
		otlpEndpoint = "http://otel-collector.monitoring.svc.cluster.local:4318"
	}
	endpointHost := strings.TrimPrefix(strings.TrimPrefix(otlpEndpoint, "http://"), "https://")

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceNameKey.String(serviceName),
			semconv.ServiceVersionKey.String("1.0.0"),
			attribute.String("deployment.environment", getEnv("ENVIRONMENT", "production")),
			attribute.String("service.namespace", "togglemaster"),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("falha ao criar resource OTel: %w", err)
	}

	// 1. Trace Exporter (OTLP HTTP -> OTel Collector)
	traceExporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpoint(endpointHost),
		otlptracehttp.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("falha ao criar trace exporter OTLP: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	otel.SetTracerProvider(tp)

	// Configura propagação distribuída W3C TraceContext + Baggage
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	// 2. Metric Exporter (OTLP HTTP -> OTel Collector)
	metricExporter, err := otlpmetrichttp.New(ctx,
		otlpmetrichttp.WithEndpoint(endpointHost),
		otlpmetrichttp.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("falha ao criar metric exporter OTLP: %w", err)
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter, sdkmetric.WithInterval(10*time.Second))),
		sdkmetric.WithResource(res),
	)
	otel.SetMeterProvider(mp)

	tracer = tp.Tracer(serviceName)
	meter = mp.Meter(serviceName)

	httpRequestsCounter, _ = meter.Int64Counter(
		"http_requests_total",
		metric.WithDescription("Total de requisicoes HTTP processadas"),
	)
	httpDurationHist, _ = meter.Float64Histogram(
		"http_request_duration_seconds",
		metric.WithDescription("Duracao das requisicoes HTTP em segundos"),
		metric.WithUnit("s"),
	)

	logStruct("INFO", "OpenTelemetry inicializado com sucesso", map[string]interface{}{
		"service":       serviceName,
		"otlp_endpoint": otlpEndpoint,
	})

	shutdown := func(shutdownCtx context.Context) error {
		_ = mp.Shutdown(shutdownCtx)
		return tp.Shutdown(shutdownCtx)
	}

	return shutdown, nil
}

// statusRecorder captura o HTTP status code da resposta
type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (rec *statusRecorder) WriteHeader(code int) {
	rec.statusCode = code
	rec.ResponseWriter.WriteHeader(code)
}

// telemetryMiddleware extrai o contexto de trace W3C, cria spans e registra métricas/logs estruturados
func telemetryMiddleware(serviceName string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metrics" {
			next.ServeHTTP(w, r)
			return
		}

		start := time.Now()

		// Extrai o contexto W3C TraceContext dos headers HTTP recebidos
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))

		spanName := fmt.Sprintf("%s %s", r.Method, r.URL.Path)
		if tracer == nil {
			tracer = otel.Tracer(serviceName)
		}

		ctx, span := tracer.Start(ctx, spanName,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				semconv.HTTPRequestMethodKey.String(r.Method),
				semconv.URLPath(r.URL.Path),
				attribute.String("http.route", r.URL.Path),
				attribute.String("service.name", serviceName),
			),
		)
		defer span.End()

		rec := &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(rec, r.WithContext(ctx))

		duration := time.Since(start).Seconds()
		statusStr := strconv.Itoa(rec.statusCode)

		span.SetAttributes(semconv.HTTPResponseStatusCode(rec.statusCode))
		if rec.statusCode >= 500 {
			span.SetStatus(codes.Error, fmt.Sprintf("HTTP %d", rec.statusCode))
		} else {
			span.SetStatus(codes.Ok, "OK")
		}

		attrs := metric.WithAttributes(
			attribute.String("service", serviceName),
			attribute.String("method", r.Method),
			attribute.String("route", r.URL.Path),
			attribute.String("status_code", statusStr),
		)
		if httpRequestsCounter != nil {
			httpRequestsCounter.Add(ctx, 1, attrs)
		}
		if httpDurationHist != nil {
			httpDurationHist.Record(ctx, duration, attrs)
		}

		recordPromMetric(serviceName, r.Method, r.URL.Path, statusStr, duration)

		if r.URL.Path != "/health" {
			logWithTrace(ctx, "INFO", "HTTP request processed", map[string]interface{}{
				"service":     serviceName,
				"method":      r.Method,
				"path":        r.URL.Path,
				"status_code": rec.statusCode,
				"duration_ms": float64(time.Since(start).Microseconds()) / 1000.0,
			})
		}
	})
}

func recordPromMetric(service, method, route, status string, duration float64) {
	metricsMu.Lock()
	defer metricsMu.Unlock()
	key := fmt.Sprintf(`service="%s",method="%s",route="%s",status_code="%s"`, service, method, route, status)
	promReqCounts[key]++
	promDurSums[key] += duration
	promDurCounts[key]++
}

func metricsHandler(w http.ResponseWriter, _ *http.Request) {
	metricsMu.Lock()
	defer metricsMu.Unlock()

	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = fmt.Fprintln(w, "# HELP http_requests_total Total de requisicoes HTTP processadas")
	_, _ = fmt.Fprintln(w, "# TYPE http_requests_total counter")
	for labels, count := range promReqCounts {
		_, _ = fmt.Fprintf(w, "http_requests_total{%s} %d\n", labels, count)
	}

	_, _ = fmt.Fprintln(w, "# HELP http_request_duration_seconds_sum Soma da duracao das requisicoes HTTP")
	_, _ = fmt.Fprintln(w, "# TYPE http_request_duration_seconds_sum counter")
	for labels, sum := range promDurSums {
		_, _ = fmt.Fprintf(w, "http_request_duration_seconds_sum{%s} %.6f\n", labels, sum)
	}

	_, _ = fmt.Fprintln(w, "# HELP http_request_duration_seconds_count Contagem de requisicoes HTTP medidas")
	_, _ = fmt.Fprintln(w, "# TYPE http_request_duration_seconds_count counter")
	for labels, count := range promDurCounts {
		_, _ = fmt.Fprintf(w, "http_request_duration_seconds_count{%s} %d\n", labels, count)
	}
}

// logWithTrace emite logs em formato JSON estruturado com trace_id e span_id para o Loki / OTel
func logWithTrace(ctx context.Context, level, msg string, fields map[string]interface{}) {
	if fields == nil {
		fields = make(map[string]interface{})
	}
	spanCtx := trace.SpanFromContext(ctx).SpanContext()
	if spanCtx.HasTraceID() {
		fields["trace_id"] = spanCtx.TraceID().String()
	}
	if spanCtx.HasSpanID() {
		fields["span_id"] = spanCtx.SpanID().String()
	}
	logStruct(level, msg, fields)
}

func logStruct(level, msg string, fields map[string]interface{}) {
	payload := map[string]interface{}{
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"level":     level,
		"message":   msg,
		"service":   getEnv("OTEL_SERVICE_NAME", "auth-service"),
	}
	for k, v := range fields {
		payload[k] = v
	}
	b, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[%s] %s", level, msg)
		return
	}
	log.Println(string(b))
}

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}
