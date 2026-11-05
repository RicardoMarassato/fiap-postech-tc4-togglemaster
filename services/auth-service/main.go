package main

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	_ "github.com/jackc/pgx/v4/stdlib"
	"github.com/joho/godotenv"
)

// App struct (para injeção de dependência)
type App struct {
	DB        *sql.DB
	MasterKey string
	ChaosMode atomic.Bool
}

func main() {
	// Carrega o .env para desenvolvimento local. Em produção, isso não fará nada.
	_ = godotenv.Load()

	// --- Inicializa OpenTelemetry ---
	serviceName := getEnv("OTEL_SERVICE_NAME", "auth-service")
	shutdownTelemetry, err := initTelemetry(context.Background(), serviceName)
	if err != nil {
		log.Printf("Aviso: falha ao inicializar OpenTelemetry: %v", err)
	} else {
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = shutdownTelemetry(ctx)
		}()
	}

	// --- Configuração ---
	port := os.Getenv("PORT")
	if port == "" {
		port = "8001" // Porta padrão
	}

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("DATABASE_URL deve ser definida")
	}

	masterKey := os.Getenv("MASTER_KEY")
	if masterKey == "" {
		log.Fatal("MASTER_KEY deve ser definida")
	}

	// --- Conexão com o Banco ---
	db, err := connectDB(databaseURL)
	if err != nil {
		log.Fatalf("Não foi possível conectar ao banco de dados: %v", err)
	}
	defer db.Close()

	app := &App{
		DB:        db,
		MasterKey: masterKey,
	}

	// --- Rotas da API ---
	mux := http.NewServeMux()
	mux.HandleFunc("/health", app.healthHandler)
	mux.HandleFunc("/metrics", metricsHandler)

	// Endpoint público para validar uma chave
	mux.HandleFunc("/validate", app.validateKeyHandler)

	// Endpoint de Chaos Engineering para demonstrar Incidente e Self-Healing na Fase 4
	mux.HandleFunc("/chaos", app.chaosHandler)

	// Endpoints de "admin" para criar/gerenciar chaves
	// Eles são protegidos pelo middleware de autenticação
	mux.Handle("/admin/keys", app.masterKeyAuthMiddleware(http.HandlerFunc(app.createKeyHandler)))

	// Envolve o mux com o middleware de telemetria OpenTelemetry
	instrumentedHandler := telemetryMiddleware(serviceName, mux)

	logStruct("INFO", "Serviço de Autenticação (Go) iniciado", map[string]interface{}{
		"port": port,
	})
	server := &http.Server{
		Addr:              ":" + port,
		Handler:           instrumentedHandler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

// connectDB inicializa e testa a conexão com o PostgreSQL
func connectDB(databaseURL string) (*sql.DB, error) {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, err
	}

	if err = db.Ping(); err != nil {
		return nil, err
	}

	schema := `CREATE TABLE IF NOT EXISTS api_keys (
		id SERIAL PRIMARY KEY,
		name VARCHAR(100) NOT NULL,
		key_hash VARCHAR(64) NOT NULL UNIQUE,
		is_active BOOLEAN DEFAULT true,
		created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
	);`
	if _, err = db.Exec(schema); err != nil {
		return nil, err
	}

	logStruct("INFO", "Conectado ao PostgreSQL e schema validado com sucesso!", nil)
	return db, nil
}
