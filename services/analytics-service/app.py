import os
import sys
import threading
import json
import uuid
import time
import logging
import boto3
from botocore.exceptions import NoCredentialsError, ClientError
from flask import Flask, jsonify
from dotenv import load_dotenv
from opentelemetry import trace
from opentelemetry.trace import SpanKind, Status, StatusCode
from telemetry import (
    setup_telemetry,
    extract_sqs_trace_context,
    record_analytics_event,
)

# Configura o logging
logging.basicConfig(level=logging.INFO, format='%(asctime)s - %(levelname)s - %(message)s')
log = logging.getLogger(__name__)

# Carrega .env para desenvolvimento local
load_dotenv()

# --- Configuração ---
AWS_REGION = os.getenv("AWS_REGION")
SQS_QUEUE_URL = os.getenv("AWS_SQS_URL")
DYNAMODB_TABLE_NAME = os.getenv("AWS_DYNAMODB_TABLE")

if not all([AWS_REGION, SQS_QUEUE_URL, DYNAMODB_TABLE_NAME]):
    log.critical("Erro: AWS_REGION, AWS_SQS_URL, e AWS_DYNAMODB_TABLE devem ser definidos.")
    sys.exit(1)

# --- Servidor Flask (Health Check + Métricas + OTel) ---
app = Flask(__name__)
setup_telemetry(app, os.getenv("OTEL_SERVICE_NAME", "analytics-service"))
tracer = trace.get_tracer("analytics-service")

# --- Clientes Boto3 ---
# Criamos a sessão uma vez
try:
    session = boto3.Session(region_name=AWS_REGION)
    sqs_client = session.client("sqs")
    dynamodb_client = session.client("dynamodb")
    log.info(f"Clientes Boto3 inicializados na região {AWS_REGION}")
except NoCredentialsError:
    log.critical("Credenciais da AWS não encontradas. Verifique seu ambiente.")
    sys.exit(1)
except Exception as e:
    log.critical(f"Erro ao inicializar o Boto3: {e}")
    sys.exit(1)


# --- SQS Worker ---

def process_message(message):
    """ Processa uma única mensagem SQS e a insere no DynamoDB propagando o Trace Distribuído """
    flag_name = "unknown"
    try:
        body = json.loads(message['Body'])
        flag_name = body.get('flag_name', 'unknown')
        parent_ctx = extract_sqs_trace_context(message, body)

        with tracer.start_as_current_span(
            "sqs.process_message",
            context=parent_ctx,
            kind=SpanKind.CONSUMER,
            attributes={
                "messaging.system": "aws.sqs",
                "messaging.operation": "process",
                "messaging.message.id": message.get("MessageId", ""),
                "peer.service": "evaluation-service",
                "flag.name": flag_name,
                "user.id": body.get("user_id", ""),
            },
        ) as span:
            log.info(f"Processando mensagem ID: {message['MessageId']} (Flag: {flag_name})")

            # Gera um ID único para o item no DynamoDB
            event_id = str(uuid.uuid4())

            # Constrói o item no formato do DynamoDB
            item = {
                'id': {'S': event_id},
                'user_id': {'S': body['user_id']},
                'flag_name': {'S': body['flag_name']},
                'result': {'BOOL': body['result']},
                'timestamp': {'S': body['timestamp']}
            }

            with tracer.start_as_current_span(
                "dynamodb.put_item",
                kind=SpanKind.CLIENT,
                attributes={
                    "db.system": "dynamodb",
                    "db.operation": "PutItem",
                    "db.sql.table": DYNAMODB_TABLE_NAME,
                    "peer.service": "dynamodb",
                },
            ):
                # Insere no DynamoDB
                dynamodb_client.put_item(
                    TableName=DYNAMODB_TABLE_NAME,
                    Item=item
                )

            log.info(f"Evento {event_id} (Flag: {body['flag_name']}) salvo no DynamoDB.")

            # Se tudo deu certo, deleta a mensagem da fila
            sqs_client.delete_message(
                QueueUrl=SQS_QUEUE_URL,
                ReceiptHandle=message['ReceiptHandle']
            )
            record_analytics_event(flag_name, success=True)
            span.set_status(Status(StatusCode.OK))

    except json.JSONDecodeError:
        record_analytics_event(flag_name, success=False)
        log.error(f"Erro ao decodificar JSON da mensagem ID: {message.get('MessageId')}")
        # Não deleta a mensagem, pode ser uma "poison pill"
    except ClientError as e:
        record_analytics_event(flag_name, success=False)
        log.error(f"Erro do Boto3 (DynamoDB ou SQS) ao processar {message.get('MessageId')}: {e}")
        # Não deleta a mensagem, tenta novamente
    except Exception as e:
        record_analytics_event(flag_name, success=False)
        log.error(f"Erro inesperado ao processar {message.get('MessageId')}: {e}")
        # Não deleta a mensagem, tenta novamente

def sqs_worker_loop():
    """ Loop principal do worker que ouve a fila SQS """
    log.info("Iniciando o worker SQS...")
    while True:
        try:
            # Long-polling: espera até 20s por mensagens
            response = sqs_client.receive_message(
                QueueUrl=SQS_QUEUE_URL,
                MaxNumberOfMessages=10,  # Processa em lotes de até 10
                WaitTimeSeconds=20,
                MessageAttributeNames=['All']
            )

            messages = response.get('Messages', [])
            if not messages:
                # Nenhuma mensagem, continua o loop
                continue

            log.info(f"Recebidas {len(messages)} mensagens.")

            for message in messages:
                process_message(message)

        except ClientError as e:
            log.error(f"Erro do Boto3 no loop principal do SQS: {e}")
            time.sleep(10)  # Pausa antes de tentar novamente
        except Exception as e:
            log.error(f"Erro inesperado no loop principal do SQS: {e}")
            time.sleep(10)


@app.route('/health')
def health():
    # Uma verificação de saúde real poderia checar a conexão com o DynamoDB/SQS
    return jsonify({"status": "ok"})

# --- Inicialização ---

def start_worker():
    """ Inicia o worker SQS em uma thread separada """
    worker_thread = threading.Thread(target=sqs_worker_loop, daemon=True)
    worker_thread.start()

# Inicia o worker SQS em uma thread de background
# Isso garante que ele inicie tanto com 'flask run' quanto com 'gunicorn'
start_worker()

if __name__ == '__main__':
    port = int(os.getenv("PORT", 8005))
    app.run(host='0.0.0.0', port=port, debug=False)
