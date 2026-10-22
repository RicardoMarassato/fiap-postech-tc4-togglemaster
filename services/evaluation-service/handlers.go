package main

import (
	"context"
	"encoding/json"
	"net/http"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

type EvaluationResponse struct {
	FlagName string `json:"flag_name"`
	UserID   string `json:"user_id"`
	Result   bool   `json:"result"`
}

func (a *App) healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// chaosHandler ativa ou desativa a injeção de falhas HTTP 500 em memória para a prova real de Self-Healing
func (a *App) chaosHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	enableParam := r.URL.Query().Get("enable")
	if enableParam == "true" || enableParam == "1" {
		a.ChaosMode.Store(true)
		logWithTrace(r.Context(), "ERROR", "CHAOS MODE ATIVADO: evaluation-service passará a retornar HTTP 500 para disparar o alerta e Self-Healing", map[string]interface{}{
			"chaos_enabled": true,
		})
	} else if enableParam == "false" || enableParam == "0" {
		a.ChaosMode.Store(false)
		logWithTrace(r.Context(), "INFO", "CHAOS MODE DESATIVADO: evaluation-service restaurado manualmente", map[string]interface{}{
			"chaos_enabled": false,
		})
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"service":       "evaluation-service",
		"chaos_enabled": a.ChaosMode.Load(),
	})
}

func (a *App) evaluationHandler(w http.ResponseWriter, r *http.Request) {
	reqCtx := r.Context()
	w.Header().Set("Content-Type", "application/json")

	if a.ChaosMode.Load() {
		span := trace.SpanFromContext(reqCtx)
		span.SetStatus(codes.Error, "Chaos Engineering: Falha silenciosa/critica simulada no evaluation-service")
		logWithTrace(reqCtx, "ERROR", "Falha crítica no evaluation-service (Chaos Mode ativo) - HTTP 500", map[string]interface{}{
			"error": "evaluation worker pool timeout (simulated incident)",
		})
		http.Error(w, `{"error": "Erro interno crítico no serviço de avaliação"}`, http.StatusInternalServerError)
		return
	}

	// 1. Parsear os query parameters
	userID := r.URL.Query().Get("user_id")
	flagName := r.URL.Query().Get("flag_name")
	skipCache := r.URL.Query().Get("nocache") == "true" || r.Header.Get("Cache-Control") == "no-cache"

	if userID == "" || flagName == "" {
		http.Error(w, `{"error": "user_id e flag_name são obrigatórios"}`, http.StatusBadRequest)
		return
	}

	span := trace.SpanFromContext(reqCtx)
	span.SetAttributes(
		attribute.String("flag.name", flagName),
		attribute.String("user.id", userID),
	)

	// 2. Obter a decisão (lógica de cache/serviço está em evaluator.go)
	result, err := a.getDecision(reqCtx, userID, flagName, skipCache)
	if err != nil {
		// Se o erro for "não encontrado", retornamos 'false' (comportamento seguro)
		if _, ok := err.(*NotFoundError); ok {
			result = false
		} else {
			// Outros erros (serviços offline, etc)
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			logWithTrace(reqCtx, "ERROR", "Erro ao avaliar flag", map[string]interface{}{
				"flag_name": flagName,
				"user_id":   userID,
				"error":     err.Error(),
			})
			http.Error(w, `{"error": "Erro interno ao avaliar a flag"}`, http.StatusBadGateway)
			return
		}
	}

	if flagEvalCounter != nil {
		flagEvalCounter.Add(reqCtx, 1, metric.WithAttributes(
			attribute.String("flag_name", flagName),
			attribute.Bool("result", result),
		))
	}

	// 3. Enviar evento para SQS (assincronamente) preservando o contexto do Trace Distribuído
	bgCtx := trace.ContextWithSpan(context.Background(), span)
	go a.sendEvaluationEvent(bgCtx, userID, flagName, result)

	// 4. Retornar a resposta
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(EvaluationResponse{
		FlagName: flagName,
		UserID:   userID,
		Result:   result,
	})
}
