package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
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
	"go.opentelemetry.io/otel/trace"
)

var serviceBTracer = otel.Tracer("service-b")

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
			semconv.ServiceName("service-b"),
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

var validCEP = regexp.MustCompile(`^\d{8}$`)

func weatherHandler(responseWriter http.ResponseWriter, request *http.Request) {
	cepValue := request.URL.Path[1:]

	if !validCEP.MatchString(cepValue) {
		responseWriter.WriteHeader(http.StatusUnprocessableEntity)
		responseWriter.Write([]byte("invalid zipcode"))
		return
	}

	viaCEPContext, viaCEPSpan := serviceBTracer.Start(request.Context(), "viacep-lookup", trace.WithSpanKind(trace.SpanKindClient))

	viaCEPBaseURL := os.Getenv("VIA_CEP_BASE_URL")
	if viaCEPBaseURL == "" {
		viaCEPBaseURL = "https://viacep.com.br"
	}
	viaCEPURL := viaCEPBaseURL + "/ws/" + cepValue + "/json/"
	viaCEPRequest, _ := http.NewRequestWithContext(viaCEPContext, http.MethodGet, viaCEPURL, nil)
	viaCEPResponse, err := http.DefaultClient.Do(viaCEPRequest)
	viaCEPSpan.End()

	if err != nil {
		http.Error(responseWriter, "failed to lookup CEP", http.StatusInternalServerError)
		return
	}
	defer viaCEPResponse.Body.Close()

	var viaCEPData struct {
		Localidade string `json:"localidade"`
		Erro       bool   `json:"erro"`
	}
	json.NewDecoder(viaCEPResponse.Body).Decode(&viaCEPData)

	if viaCEPData.Erro {
		responseWriter.WriteHeader(http.StatusNotFound)
		responseWriter.Write([]byte("can not find zipcode"))
		return
	}

	weatherAPIKey := os.Getenv("WEATHER_API_KEY")
	encodedCityName := url.QueryEscape(viaCEPData.Localidade)
	weatherAPIBaseURL := os.Getenv("WEATHER_API_BASE_URL")
	if weatherAPIBaseURL == "" {
		weatherAPIBaseURL = "http://api.weatherapi.com"
	}
	weatherAPIURL := weatherAPIBaseURL + "/v1/current.json?key=" + weatherAPIKey + "&q=" + encodedCityName + "&aqi=no"

	weatherContext, weatherSpan := serviceBTracer.Start(request.Context(), "weatherapi-lookup", trace.WithSpanKind(trace.SpanKindClient))

	weatherRequest, _ := http.NewRequestWithContext(weatherContext, http.MethodGet, weatherAPIURL, nil)
	weatherResponse, err := http.DefaultClient.Do(weatherRequest)
	weatherSpan.End()

	if err != nil {
		http.Error(responseWriter, "failed to get weather", http.StatusInternalServerError)
		return
	}
	defer weatherResponse.Body.Close()

	var weatherData struct {
		Current struct {
			TempC float64 `json:"temp_c"`
		} `json:"current"`
	}
	json.NewDecoder(weatherResponse.Body).Decode(&weatherData)

	tempCelsius := weatherData.Current.TempC
	tempFahrenheit := tempCelsius*1.8 + 32
	tempKelvin := tempCelsius + 273

	responseWriter.Header().Set("Content-Type", "application/json")
	responseWriter.WriteHeader(http.StatusOK)

	json.NewEncoder(responseWriter).Encode(struct {
		City  string  `json:"city"`
		TempC float64 `json:"temp_C"`
		TempF float64 `json:"temp_F"`
		TempK float64 `json:"temp_K"`
	}{
		City:  viaCEPData.Localidade,
		TempC: tempCelsius,
		TempF: tempFahrenheit,
		TempK: tempKelvin,
	})
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

	serverAddr := os.Getenv("SERVER_ADDR")
	if serverAddr == "" {
		serverAddr = ":8081"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", weatherHandler)

	instrumentedHandler := otelhttp.NewHandler(mux, "service-b")
	log.Printf("Service B listening on %s", serverAddr)
	log.Fatal(http.ListenAndServe(serverAddr, instrumentedHandler))
}
