import unittest
from app import app


class TestSelfHealingService(unittest.TestCase):
    def setUp(self):
        self.client = app.test_client()

    def test_health(self):
        resp = self.client.get("/health")
        self.assertEqual(resp.status_code, 200)

    def test_webhook_firing(self):
        payload = {
            "status": "firing",
            "alerts": [
                {
                    "status": "firing",
                    "labels": {
                        "alertname": "AuthServiceHigh5xxErrorRate",
                        "service": "auth-service",
                        "namespace": "togglemaster",
                    },
                }
            ],
        }
        resp = self.client.post("/webhook", json=payload)
        self.assertEqual(resp.status_code, 200)
        data = resp.get_json()
        self.assertEqual(len(data["actions_taken"]), 1)
        self.assertEqual(data["actions_taken"][0]["service"], "auth-service")


if __name__ == "__main__":
    unittest.main()
