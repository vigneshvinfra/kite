package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// Prometheus metrics — required for Part 4 metrics requirement.
// Histogram buckets tuned for p50/p95 visibility (bonus: latency breakdown).
var (
	httpRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total HTTP requests by method, path, and status.",
		},
		[]string{"method", "path", "status"},
	)

	httpRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request latency in seconds.",
			Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
		},
		[]string{"method", "path"},
	)

	httpInFlight = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "http_in_flight_requests",
			Help: "Current number of in-flight HTTP requests.",
		},
	)
)

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// instrument records Prometheus metrics + structured access logs per request.
func instrument(path string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		httpInFlight.Inc()
		defer httpInFlight.Dec()

		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		duration := time.Since(start).Seconds()
		httpRequestsTotal.WithLabelValues(r.Method, path, strconv.Itoa(rec.status)).Inc()
		httpRequestDuration.WithLabelValues(r.Method, path).Observe(duration)

		slog.Info("request",
			"method", r.Method,
			"path", path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	}
}

// initTracer wires OpenTelemetry to an OTLP gRPC collector.
// Endpoint configured via OTEL_EXPORTER_OTLP_ENDPOINT env var.
func initTracer(ctx context.Context) (*sdktrace.TracerProvider, error) {
	exporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, err
	}
    // Setup the application resource. This is the way in which the application is identified in OpenTelemetry.
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName("myapp"),
			semconv.ServiceVersion(os.Getenv("APP_VERSION")),
			semconv.DeploymentEnvironment(os.Getenv("ENV")),
		),
	)
	if err != nil {
		return nil, err
	}
    // Create the tracer provier
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	return tp, nil
}

func main() {
	// Structured JSON logs
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	// Signal context — drives both tracer flush and HTTP shutdown
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// OpenTelemetry — only if collector endpoint is configured
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" {
		tp, err := initTracer(ctx)
		if err != nil {
			slog.Error("tracer init failed", "err", err)
		} else {
			defer func() {
				if err := tp.Shutdown(context.Background()); err != nil {
					slog.Error("tracer shutdown failed", "err", err)
				}
			}()
		}
	}

	mux := http.NewServeMux()

	// Root route — instrumented for metrics, structured logs, and traces
	mux.Handle("/", otelhttp.NewHandler(instrument("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello kite\n"))
	}), "root"))

	// Probes — metrics + logs, but no tracing (high-frequency, not interesting)
	mux.HandleFunc("/health", instrument("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok\n"))
	}))
	mux.HandleFunc("/ready", instrument("/ready", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ready\n"))
	}))

	// Prometheus scrape endpoint — not instrumented (no recursive metrics)
	mux.Handle("/metrics", promhttp.Handler())

	srv := &http.Server{
		Addr:              ":8000",
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Run server in goroutine so main can wait on signal
	go func() {
		slog.Info("server listening", "port", 8000)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server cannot listen", "err", err)
			os.Exit(1)
		}
	}()

	// Wait for SIGTERM/SIGINT
    slog.Info("awaiting shutdown signal")
	<-ctx.Done()
	slog.Info("shutdown signal received")

	// Drain in-flight requests, then exit (tracer flushes via deferred Shutdown)
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown failed", "err", err)
	}
}
