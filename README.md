# OpenTelemetry Distributed Tracing

Two Go microservices with OpenTelemetry distributed tracing, an OTEL Collector, and Zipkin.

## Architecture

- **Service A** (port 8080): Receives a CEP, validates it, and forwards to Service B
- **Service B** (port 8081): Receives a CEP, looks up the city via ViaCEP, and fetches temperature via WeatherAPI
- **OTEL Collector** (ports 4317/4318): Receives traces from both services and exports to Zipkin
- **Zipkin** (port 9411): Distributed tracing UI

## Required Environment Variables

- `WEATHER_API_KEY`: API key from [WeatherAPI](https://www.weatherapi.com/)

## How to Run

```bash
WEATHER_API_KEY=your-key-here docker-compose up --build
```

## How to Test

```bash
curl -X POST localhost:8080 \
  -H 'Content-Type: application/json' \
  -d '{"cep":"01310100"}'
```

## How to View Traces

Open http://localhost:9411 in your browser to view distributed traces in Zipkin.

## Validation Rules

- CEP must be exactly 8 digits (e.g., `01310100`)
- Returns 422 for invalid CEP format
- Returns 404 if CEP is not found
