package main

import (
	"context"
	"encoding/json"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/sqs"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// Evento que será enviado para a fila (inclui Traceparent para Distributed Tracing até o analytics-service)
type EvaluationEvent struct {
	UserID      string    `json:"user_id"`
	FlagName    string    `json:"flag_name"`
	Result      bool      `json:"result"`
	Timestamp   time.Time `json:"timestamp"`
	Traceparent string    `json:"traceparent,omitempty"`
}

// sendEvaluationEvent envia um evento para a fila SQS propagando o contexto de trace
func (a *App) sendEvaluationEvent(reqCtx context.Context, userID, flagName string, result bool) {
	tr := otel.Tracer("evaluation-service")
	sqsCtx, span := tr.Start(reqCtx, "sqs.send_message",
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			attribute.String("messaging.system", "aws.sqs"),
			attribute.String("messaging.operation", "publish"),
			attribute.String("messaging.destination.name", "togglemaster-events"),
			attribute.String("peer.service", "analytics-service"),
			attribute.String("flag.name", flagName),
			attribute.String("user.id", userID),
		),
	)
	defer span.End()

	// Extrai o traceparent W3C para enviar junto à mensagem SQS
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(sqsCtx, carrier)
	traceparent := carrier.Get("traceparent")

	// Se a URL da fila não foi configurada, apenas loga localmente e sai.
	if a.SqsSvc == nil || a.SqsQueueURL == "" {
		logWithTrace(sqsCtx, "INFO", "[SQS_DISABLED] Evento de avaliação gerado localmente", map[string]interface{}{
			"user_id":     userID,
			"flag_name":   flagName,
			"result":      result,
			"traceparent": traceparent,
		})
		return
	}

	event := EvaluationEvent{
		UserID:      userID,
		FlagName:    flagName,
		Result:      result,
		Timestamp:   time.Now().UTC(),
		Traceparent: traceparent,
	}

	body, err := json.Marshal(event)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		logWithTrace(sqsCtx, "ERROR", "Erro ao serializar evento SQS", map[string]interface{}{
			"error": err.Error(),
		})
		return
	}

	msgInput := &sqs.SendMessageInput{
		MessageBody: aws.String(string(body)),
		QueueUrl:    aws.String(a.SqsQueueURL),
	}
	if traceparent != "" {
		msgInput.MessageAttributes = map[string]*sqs.MessageAttributeValue{
			"traceparent": {
				DataType:    aws.String("String"),
				StringValue: aws.String(traceparent),
			},
		}
	}

	// Envia a mensagem
	_, err = a.SqsSvc.SendMessage(msgInput)

	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		logWithTrace(sqsCtx, "ERROR", "Erro ao enviar mensagem para SQS", map[string]interface{}{
			"error": err.Error(),
		})
	} else {
		logWithTrace(sqsCtx, "INFO", "Evento de avaliação enviado para SQS com TraceContext", map[string]interface{}{
			"flag_name":   flagName,
			"traceparent": traceparent,
		})
	}
}
