package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

func initTracerProvider(ctx context.Context) (*sdktrace.TracerProvider, error) {
	collectorEndpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if collectorEndpoint == "" {
		collectorEndpoint = "otel-collector:4317"
	}

	traceExporter, err := otlptracegrpc.New(
		ctx,
		otlptracegrpc.WithEndpoint(collectorEndpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, err
	}

	serviceResource, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName("service-a"),
		),
	)
	if err != nil {
		return nil, err
	}

	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(serviceResource),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)

	otel.SetTracerProvider(tracerProvider)
	otel.SetTextMapPropagator(
		propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		),
	)

	return tracerProvider, nil
}

func main() {
	ctx := context.Background()

	tracerProvider, err := initTracerProvider(ctx)
	if err != nil {
		log.Fatalf("failed to initialize tracer provider: %v", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		tracerProvider.Shutdown(shutdownCtx)
	}()

	instrumentedClient := &http.Client{
		Transport: otelhttp.NewTransport(http.DefaultTransport),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var requestBody struct {
			CEP string `json:"cep"`
		}
		if err := json.NewDecoder(request.Body).Decode(&requestBody); err != nil {
			http.Error(responseWriter, "invalid request body", http.StatusBadRequest)
			return
		}

		cepPattern := regexp.MustCompile(`^\d{8}$`)
		if !cepPattern.MatchString(requestBody.CEP) {
			responseWriter.WriteHeader(http.StatusUnprocessableEntity)
			responseWriter.Write([]byte("invalid zipcode"))
			return
		}

		serviceBBaseURL := os.Getenv("SERVICE_B_URL")
		if serviceBBaseURL == "" {
			serviceBBaseURL = "http://service-b:8081"
		}

		forwardURL := serviceBBaseURL + "/" + requestBody.CEP
		forwardRequest, err := http.NewRequestWithContext(request.Context(), http.MethodGet, forwardURL, nil)
		if err != nil {
			http.Error(responseWriter, "internal error", http.StatusInternalServerError)
			return
		}

		serviceBResponse, err := instrumentedClient.Do(forwardRequest)
		if err != nil {
			http.Error(responseWriter, "service b unavailable", http.StatusServiceUnavailable)
			return
		}
		defer serviceBResponse.Body.Close()

		responseWriter.WriteHeader(serviceBResponse.StatusCode)
		responseWriter.Header().Set("Content-Type", "application/json")
		io.Copy(responseWriter, serviceBResponse.Body)
	})

	serverAddr := os.Getenv("SERVER_ADDR")
	if serverAddr == "" {
		serverAddr = ":8080"
	}

	instrumentedHandler := otelhttp.NewHandler(mux, "service-a")
	log.Printf("Service A listening on %s", serverAddr)
	log.Fatal(http.ListenAndServe(serverAddr, instrumentedHandler))
}
