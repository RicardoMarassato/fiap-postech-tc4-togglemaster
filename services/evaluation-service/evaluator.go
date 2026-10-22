package main

import (
	"context"
	"crypto/sha1" // #nosec G505 -- usado apenas para bucketing determinístico (não criptográfico)
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const (
	// Tempo de vida do cache em segundos
	CACHE_TTL = 30 * time.Second
)

// getDecision é o wrapper principal
func (a *App) getDecision(reqCtx context.Context, userID, flagName string, skipCache bool) (bool, error) {
	tr := otel.Tracer("evaluation-service")
	evalCtx, span := tr.Start(reqCtx, "flag.evaluate",
		trace.WithAttributes(
			attribute.String("flag.name", flagName),
			attribute.String("user.id", userID),
			attribute.Bool("cache.skip", skipCache),
		),
	)
	defer span.End()

	// 1. Obter os dados da flag (do cache ou dos serviços)
	info, err := a.getCombinedFlagInfo(evalCtx, flagName, skipCache)
	if err != nil {
		span.RecordError(err)
		return false, err
	}

	// 2. Executar a lógica de avaliação
	decision := a.runEvaluationLogic(evalCtx, info, userID)
	span.SetAttributes(attribute.Bool("flag.result", decision))
	return decision, nil
}

// getCombinedFlagInfo busca os dados no Redis, com fallback para os microsserviços
func (a *App) getCombinedFlagInfo(reqCtx context.Context, flagName string, skipCache bool) (*CombinedFlagInfo, error) {
	cacheKey := fmt.Sprintf("flag_info:%s", flagName)
	tr := otel.Tracer("evaluation-service")

	// 1. Tentar buscar do Cache (Redis) se skipCache não estiver ativo
	if !skipCache && a.RedisClient != nil {
		redisCtx, redisSpan := tr.Start(reqCtx, "redis.get",
			trace.WithSpanKind(trace.SpanKindClient),
			trace.WithAttributes(
				attribute.String("db.system", "redis"),
				attribute.String("db.operation", "GET"),
				attribute.String("db.redis.key", cacheKey),
				attribute.String("peer.service", "redis"),
			),
		)
		val, err := a.RedisClient.Get(redisCtx, cacheKey).Result()
		if err == nil {
			redisSpan.SetAttributes(attribute.Bool("cache.hit", true))
			redisSpan.End()
			var info CombinedFlagInfo
			if err := json.Unmarshal([]byte(val), &info); err == nil {
				logWithTrace(reqCtx, "INFO", "Cache HIT para flag", map[string]interface{}{
					"flag_name": flagName,
					"cache_hit": true,
				})
				return &info, nil
			}
			logWithTrace(reqCtx, "WARN", "Erro ao desserializar cache para flag", map[string]interface{}{
				"flag_name": flagName,
				"error":     err.Error(),
			})
		} else {
			redisSpan.SetAttributes(attribute.Bool("cache.hit", false))
			redisSpan.End()
		}
	}

	logWithTrace(reqCtx, "INFO", "Cache MISS para flag - consultando flag-service e targeting-service", map[string]interface{}{
		"flag_name": flagName,
		"cache_hit": false,
	})

	// 2. Cache MISS - Buscar dos serviços propagando o TraceContext
	info, err := a.fetchFromServices(reqCtx, flagName)
	if err != nil {
		return nil, err
	}

	// 3. Salvar no Cache
	if a.RedisClient != nil {
		jsonData, err := json.Marshal(info)
		if err == nil {
			redisSetCtx, redisSetSpan := tr.Start(reqCtx, "redis.set",
				trace.WithSpanKind(trace.SpanKindClient),
				trace.WithAttributes(
					attribute.String("db.system", "redis"),
					attribute.String("db.operation", "SET"),
					attribute.String("db.redis.key", cacheKey),
					attribute.String("peer.service", "redis"),
				),
			)
			_ = a.RedisClient.Set(redisSetCtx, cacheKey, jsonData, CACHE_TTL).Err()
			redisSetSpan.End()
		}
	}

	return info, nil
}

// fetchFromServices busca dados do flag-service e targeting-service concorrentemente
func (a *App) fetchFromServices(reqCtx context.Context, flagName string) (*CombinedFlagInfo, error) {
	var wg sync.WaitGroup
	wg.Add(2)

	var flagInfo *Flag
	var ruleInfo *TargetingRule
	var flagErr, ruleErr error

	// Goroutine 1: Buscar do flag-service
	go func() {
		defer wg.Done()
		flagInfo, flagErr = a.fetchFlag(reqCtx, flagName)
	}()

	// Goroutine 2: Buscar do targeting-service
	go func() {
		defer wg.Done()
		ruleInfo, ruleErr = a.fetchRule(reqCtx, flagName)
	}()

	wg.Wait()

	if flagErr != nil {
		return nil, flagErr
	}
	if ruleErr != nil {
		logWithTrace(reqCtx, "INFO", "Aviso: Nenhuma regra de segmentação encontrada. Usando padrão.", map[string]interface{}{
			"flag_name": flagName,
		})
	}

	return &CombinedFlagInfo{
		Flag: flagInfo,
		Rule: ruleInfo,
	}, nil
}

// fetchFlag chama o flag-service propagando o header W3C traceparent
func (a *App) fetchFlag(reqCtx context.Context, flagName string) (*Flag, error) {
	url := fmt.Sprintf("%s/flags/%s", a.FlagServiceURL, flagName)
	tr := otel.Tracer("evaluation-service")
	clientCtx, span := tr.Start(reqCtx, "HTTP GET flag-service /flags/{name}",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("http.method", "GET"),
			attribute.String("http.url", url),
			attribute.String("peer.service", "flag-service"),
		),
	)
	defer span.End()

	apiKey := os.Getenv("SERVICE_API_KEY")
	req, err := http.NewRequestWithContext(clientCtx, "GET", url, nil)
	if err != nil {
		span.RecordError(err)
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	// Injeta o contexto W3C TraceContext nos headers HTTP de saída
	otel.GetTextMapPropagator().Inject(clientCtx, propagation.HeaderCarrier(req.Header))

	resp, err := a.HttpClient.Do(req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, fmt.Errorf("erro ao chamar flag-service: %w", err)
	}
	defer resp.Body.Close()

	span.SetAttributes(attribute.Int("http.status_code", resp.StatusCode))
	if resp.StatusCode == http.StatusNotFound {
		return nil, &NotFoundError{flagName}
	}
	if resp.StatusCode != http.StatusOK {
		span.SetStatus(codes.Error, fmt.Sprintf("HTTP %d", resp.StatusCode))
		return nil, fmt.Errorf("flag-service retornou status %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	var flag Flag
	if err := json.Unmarshal(body, &flag); err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("erro ao desserializar resposta do flag-service: %w", err)
	}
	return &flag, nil
}

// fetchRule chama o targeting-service propagando o header W3C traceparent
func (a *App) fetchRule(reqCtx context.Context, flagName string) (*TargetingRule, error) {
	url := fmt.Sprintf("%s/rules/%s", a.TargetingServiceURL, flagName)
	tr := otel.Tracer("evaluation-service")
	clientCtx, span := tr.Start(reqCtx, "HTTP GET targeting-service /rules/{flag_name}",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("http.method", "GET"),
			attribute.String("http.url", url),
			attribute.String("peer.service", "targeting-service"),
		),
	)
	defer span.End()

	apiKey := os.Getenv("SERVICE_API_KEY")
	req, err := http.NewRequestWithContext(clientCtx, "GET", url, nil)
	if err != nil {
		span.RecordError(err)
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	// Injeta o contexto W3C TraceContext nos headers HTTP de saída
	otel.GetTextMapPropagator().Inject(clientCtx, propagation.HeaderCarrier(req.Header))

	resp, err := a.HttpClient.Do(req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, fmt.Errorf("erro ao chamar targeting-service: %w", err)
	}
	defer resp.Body.Close()

	span.SetAttributes(attribute.Int("http.status_code", resp.StatusCode))
	if resp.StatusCode == http.StatusNotFound {
		return nil, &NotFoundError{flagName} // Não é um erro fatal
	}
	if resp.StatusCode != http.StatusOK {
		span.SetStatus(codes.Error, fmt.Sprintf("HTTP %d", resp.StatusCode))
		return nil, fmt.Errorf("targeting-service retornou status %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	var rule TargetingRule
	if err := json.Unmarshal(body, &rule); err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("erro ao desserializar resposta do targeting-service: %w", err)
	}
	return &rule, nil
}

// runEvaluationLogic é onde a decisão é tomada
func (a *App) runEvaluationLogic(reqCtx context.Context, info *CombinedFlagInfo, userID string) bool {
	if info.Flag == nil || !info.Flag.IsEnabled {
		return false
	}

	if info.Rule == nil || !info.Rule.IsEnabled {
		return true
	}

	// 3. Processa a regra (só temos "PERCENTAGE" por enquanto)
	rule := info.Rule.Rules
	if rule.Type == "PERCENTAGE" {
		// Converte o 'value' (que é interface{}) para float64
		percentage, ok := rule.Value.(float64)
		if !ok {
			logWithTrace(reqCtx, "ERROR", "Valor da regra de porcentagem não é um número", map[string]interface{}{
				"flag_name": info.Flag.Name,
			})
			return false
		}

		// Calcula o "bucket" do usuário (0-99)
		userBucket := getDeterministicBucket(userID + info.Flag.Name)

		if float64(userBucket) < percentage {
			return true
		}
	}

	return false
}

func getDeterministicBucket(input string) int {
	// Usamos SHA1 (rápido) e pegamos os primeiros 4 bytes
	hasher := sha1.New() // #nosec G401 -- SHA1 usado apenas para hash de distribuição percentual
	hasher.Write([]byte(input))
	hash := hasher.Sum(nil)

	// Converte 4 bytes para um uint32
	val := binary.BigEndian.Uint32(hash[:4])

	// Retorna o módulo 100
	return int(val % 100)
}
