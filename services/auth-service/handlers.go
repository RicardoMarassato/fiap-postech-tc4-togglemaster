package main

import (
	"encoding/json"
	"net/http"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Estrutura para o corpo da requisição de criação de chave
type CreateKeyRequest struct {
	Name string `json:"name"`
}

// Estrutura para a resposta da criação de chave
type CreateKeyResponse struct {
	Name    string `json:"name"`
	Key     string `json:"key"` // A chave em texto plano é retornada APENAS uma vez
	Message string `json:"message"`
}

// healthHandler é um simples endpoint de verificação de saúde
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
		logWithTrace(r.Context(), "ERROR", "CHAOS MODE ATIVADO: auth-service passará a retornar HTTP 500 para disparar o alerta e Self-Healing", map[string]interface{}{
			"chaos_enabled": true,
		})
	} else if enableParam == "false" || enableParam == "0" {
		a.ChaosMode.Store(false)
		logWithTrace(r.Context(), "INFO", "CHAOS MODE DESATIVADO: auth-service restaurado manualmente", map[string]interface{}{
			"chaos_enabled": false,
		})
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"service":       "auth-service",
		"chaos_enabled": a.ChaosMode.Load(),
	})
}

// validateKeyHandler verifica se uma chave de API (enviada via Header) é válida
func (a *App) validateKeyHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Verifica se o modo de falha simulada (Chaos Mode) está ativo
	if a.ChaosMode.Load() {
		span := trace.SpanFromContext(ctx)
		span.SetStatus(codes.Error, "Chaos Engineering: Falha critica simulada no auth-service")
		logWithTrace(ctx, "ERROR", "Falha crítica no auth-service (Chaos Mode ativo) - HTTP 500", map[string]interface{}{
			"error": "database connection pool exhausted (simulated incident)",
		})
		http.Error(w, `{"error": "Erro interno crítico no serviço de autenticação"}`, http.StatusInternalServerError)
		return
	}

	// Extrai a chave do header "Authorization: Bearer <key>"
	authHeader := r.Header.Get("Authorization")
	keyString := strings.TrimPrefix(authHeader, "Bearer ")

	if keyString == "" {
		http.Error(w, "Authorization header não encontrado", http.StatusUnauthorized)
		return
	}

	// Calcula o hash da chave recebida
	keyHash := hashAPIKey(keyString)

	// Cria span filho para a consulta ao banco PostgreSQL
	tr := otel.Tracer("auth-service")
	dbCtx, dbSpan := tr.Start(ctx, "postgresql.select.api_keys",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "postgresql"),
			attribute.String("db.name", "auth_db"),
			attribute.String("db.operation", "SELECT"),
			attribute.String("db.sql.table", "api_keys"),
		),
	)
	defer dbSpan.End()

	// Verifica se o hash existe no banco de dados
	var id int
	err := a.DB.QueryRowContext(dbCtx, "SELECT id FROM api_keys WHERE key_hash = $1 AND is_active = true", keyHash).Scan(&id)
	if err != nil {
		dbSpan.RecordError(err)
		// Se não encontrar (sql.ErrNoRows), ou qualquer outro erro, a chave é inválida
		logWithTrace(ctx, "WARN", "Falha na validação da chave de API", map[string]interface{}{
			"key_hash_prefix": keyHash[:6],
			"error":           err.Error(),
		})
		http.Error(w, "Chave de API inválida ou inativa", http.StatusUnauthorized)
		return
	}

	// Chave válida
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"message": "Chave válida"})
}

// createKeyHandler cria uma nova chave de API
func (a *App) createKeyHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if r.Method != http.MethodPost {
		http.Error(w, "Método não permitido", http.StatusMethodNotAllowed)
		return
	}

	var req CreateKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Corpo da requisição inválido", http.StatusBadRequest)
		return
	}

	if req.Name == "" {
		http.Error(w, "O campo 'name' é obrigatório", http.StatusBadRequest)
		return
	}

	// Gera uma nova chave e seu hash
	newKey, err := generateAPIKey()
	if err != nil {
		http.Error(w, "Erro ao gerar a chave", http.StatusInternalServerError)
		return
	}
	newKeyHash := hashAPIKey(newKey)

	tr := otel.Tracer("auth-service")
	dbCtx, dbSpan := tr.Start(ctx, "postgresql.insert.api_keys",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "postgresql"),
			attribute.String("db.name", "auth_db"),
			attribute.String("db.operation", "INSERT"),
			attribute.String("db.sql.table", "api_keys"),
		),
	)
	defer dbSpan.End()

	// Salva o hash no banco de dados
	var newID int
	err = a.DB.QueryRowContext(
		dbCtx,
		"INSERT INTO api_keys (name, key_hash) VALUES ($1, $2) RETURNING id",
		req.Name, newKeyHash,
	).Scan(&newID)

	if err != nil {
		dbSpan.RecordError(err)
		dbSpan.SetStatus(codes.Error, err.Error())
		logWithTrace(ctx, "ERROR", "Erro ao salvar a chave no banco", map[string]interface{}{
			"error": err.Error(),
		})
		http.Error(w, "Erro ao salvar a chave", http.StatusInternalServerError)
		return
	}

	logWithTrace(ctx, "INFO", "Nova chave criada com sucesso", map[string]interface{}{
		"key_id":   newID,
		"key_name": req.Name,
	})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(CreateKeyResponse{
		Name:    req.Name,
		Key:     newKey, // Retorna a chave em texto plano pela última vez
		Message: "Guarde esta chave com segurança! Você não poderá vê-la novamente.",
	})
}

// --- Middleware ---

// masterKeyAuthMiddleware protege endpoints que só podem ser acessados com a MASTER_KEY
func (a *App) masterKeyAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		keyString := strings.TrimPrefix(authHeader, "Bearer ")

		if keyString != a.MasterKey {
			http.Error(w, "Acesso não autorizado", http.StatusForbidden)
			return
		}
		// Se a chave for válida, continua para o handler principal
		next.ServeHTTP(w, r)
	})
}
