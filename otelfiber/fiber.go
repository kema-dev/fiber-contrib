package otelfiber

import (
	"context"
	"net/http"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/utils/v2"
	otelcontrib "go.opentelemetry.io/contrib"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.34.0"
	oteltrace "go.opentelemetry.io/otel/trace"
)

const (
	tracerKey           = "gofiber-contrib-tracer-fiber"
	instrumentationName = "github.com/gofiber/contrib/otelfiber"

	// https://opentelemetry.io/docs/specs/semconv/http/http-metrics/#metric-httpserverrequestduration
	MetricNameHttpServerRequestDuration = "http.server.request.duration"
	// https://opentelemetry.io/docs/specs/semconv/http/http-metrics/#metric-httpserveractive_requests
	MetricNameHttpServerActiveRequests = "http.server.active_requests"
	// https://opentelemetry.io/docs/specs/semconv/http/http-metrics/#metric-httpserverrequestbodysize
	MetricNameHttpServerRequestBodySize = "http.server.request.body.size"
	// https://opentelemetry.io/docs/specs/semconv/http/http-metrics/#metric-httpserverresponsebodysize
	MetricNameHttpServerResponseBodySize = "http.server.response.body.size"

	// Unit constants for deprecated metric units
	UnitDimensionless = "1"
	UnitBytes         = "By"
	UnitRequests      = "{request}"
	UnitSeconds       = "s"
)

// Middleware returns fiber handler which will trace incoming requests.
func Middleware(opts ...Option) fiber.Handler {
	cfg := config{
		collectClientIP: true,
	}
	for _, opt := range opts {
		opt.apply(&cfg)
	}

	if cfg.TracerProvider == nil {
		cfg.TracerProvider = otel.GetTracerProvider()
	}
	tracer := cfg.TracerProvider.Tracer(
		instrumentationName,
		oteltrace.WithInstrumentationVersion(otelcontrib.Version()),
	)

	if cfg.MeterProvider == nil {
		cfg.MeterProvider = otel.GetMeterProvider()
	}
	meter := cfg.MeterProvider.Meter(
		instrumentationName,
		metric.WithInstrumentationVersion(otelcontrib.Version()),
	)

	// https://opentelemetry.io/docs/specs/semconv/http/http-metrics/#metric-httpserverrequestduration
	httpServerDuration, err := meter.Float64Histogram(
		MetricNameHttpServerRequestDuration,
		metric.WithUnit(UnitSeconds),
		metric.WithDescription("Duration of HTTP server requests."),
	)
	if err != nil {
		otel.Handle(err)
	}

	// https://opentelemetry.io/docs/specs/semconv/http/http-metrics/#metric-httpserveractive_requests
	httpServerActiveRequests, err := meter.Int64UpDownCounter(
		MetricNameHttpServerActiveRequests,
		metric.WithUnit(UnitRequests),
		metric.WithDescription(
			"Number of active HTTP server requests.",
		),
	)
	if err != nil {
		otel.Handle(err)
	}

	// https://opentelemetry.io/docs/specs/semconv/http/http-metrics/#metric-httpserverrequestbodysize
	httpServerRequestSize, err := meter.Int64Histogram(
		MetricNameHttpServerRequestBodySize,
		metric.WithUnit(UnitBytes),
		metric.WithDescription("Size of HTTP server request bodies."),
	)
	if err != nil {
		otel.Handle(err)
	}

	// https://opentelemetry.io/docs/specs/semconv/http/http-metrics/#metric-httpserverresponsebodysize
	httpServerResponseSize, err := meter.Int64Histogram(
		MetricNameHttpServerResponseBodySize,
		metric.WithUnit(UnitBytes),
		metric.WithDescription("Size of HTTP server response bodies."),
	)
	if err != nil {
		otel.Handle(err)
	}

	if cfg.Propagators == nil {
		cfg.Propagators = otel.GetTextMapPropagator()
	}
	if cfg.SpanNameFormatter == nil {
		cfg.SpanNameFormatter = defaultSpanNameFormatter
	}

	return func(c fiber.Ctx) error {
		// Don't execute middleware if Next returns true
		if cfg.Next != nil && cfg.Next(c) {
			return c.Next()
		}

		c.Locals(tracerKey, tracer)
		savedCtx, cancel := context.WithCancel(c.UserContext())

		start := time.Now()

		requestMetricsAttrs := httpServerMetricAttributesFromRequest(c, cfg)
		if !cfg.withoutMetrics {
			httpServerActiveRequests.Add(savedCtx, 1, metric.WithAttributes(requestMetricsAttrs...))
		}

		responseMetricAttrs := make([]attribute.KeyValue, len(requestMetricsAttrs))
		copy(responseMetricAttrs, requestMetricsAttrs)

		reqHeader := make(http.Header)
		c.Request().Header.VisitAll(func(k, v []byte) {
			reqHeader.Add(string(k), string(v))
		})

		ctx := cfg.Propagators.Extract(savedCtx, propagation.HeaderCarrier(reqHeader))

		opts := []oteltrace.SpanStartOption{
			oteltrace.WithAttributes(httpServerTraceAttributesFromRequest(c, cfg)...),
			oteltrace.WithSpanKind(oteltrace.SpanKindServer),
		}

		// temporary set to c.Path() first
		// update with c.Route().Path after c.Next() is called
		// to get pathRaw
		spanName := utils.CopyString(c.Path())
		ctx, span := tracer.Start(ctx, spanName, opts...)
		defer span.End()

		// pass the span through userContext
		c.SetUserContext(ctx)

		// serve the request to the next middleware
		if err := c.Next(); err != nil {
			span.RecordError(err)
			// invokes the registered HTTP error handler
			// to get the correct response status code
			_ = c.App().Config().ErrorHandler(c, err)
		}

		// extract common attributes from response
		responseAttrs := []attribute.KeyValue{
			semconv.HTTPResponseStatusCode(c.Response().StatusCode()),
			semconv.HTTPRouteKey.String(c.Route().Path), // no need to copy c.Route().Path: route strings should be immutable across app lifecycle
		}

		var responseSize int64
		requestSize := int64(len(c.Request().Body()))
		if c.GetRespHeader("Content-Type") != "text/event-stream" {
			responseSize = int64(len(c.Response().Body()))
		}

		defer func() {
			responseMetricAttrs = append(responseMetricAttrs, responseAttrs...)

			if !cfg.withoutMetrics {
				httpServerActiveRequests.Add(savedCtx, -1, metric.WithAttributes(requestMetricsAttrs...))
				httpServerDuration.Record(savedCtx, float64(time.Since(start).Microseconds())/1000, metric.WithAttributes(responseMetricAttrs...))
				httpServerRequestSize.Record(savedCtx, requestSize, metric.WithAttributes(responseMetricAttrs...))
				httpServerResponseSize.Record(savedCtx, responseSize, metric.WithAttributes(responseMetricAttrs...))
			}

			c.SetUserContext(savedCtx)
			cancel()
		}()

		span.SetAttributes(append(responseAttrs, semconv.HTTPResponseBodySizeKey.Int64(responseSize))...)
		span.SetName(cfg.SpanNameFormatter(c))

		spanStatus, spanMessage := internal.SpanStatusFromHTTPStatusCodeAndSpanKind(c.Response().StatusCode(), oteltrace.SpanKindServer)
		span.SetStatus(spanStatus, spanMessage)

		//Propagate tracing context as headers in outbound response
		tracingHeaders := make(propagation.HeaderCarrier)
		cfg.Propagators.Inject(c.UserContext(), tracingHeaders)
		for _, headerKey := range tracingHeaders.Keys() {
			c.Set(headerKey, tracingHeaders.Get(headerKey))
		}

		return nil
	}
}

// defaultSpanNameFormatter is the default formatter for spans created with the fiber
// integration. It follows [OpenTelemetry guidelines]
//
// [OpenTelemetry guidelines]: https://opentelemetry.io/docs/specs/semconv/http/http-spans/#name
func defaultSpanNameFormatter(c fiber.Ctx) string {
	method := utils.CopyString(string(c.Request().Header.Method()))

	path := utils.CopyString(string(c.Route().Path))

	// Should never happen
	if method == "" && path == "" {
		return "_OTHER"
	}

	if method != "" && path != "" {
		return method + " " + path
	}

	return method + path
}
