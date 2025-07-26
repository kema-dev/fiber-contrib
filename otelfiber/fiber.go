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
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
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

	UnitSeconds  = "s"
	UnitBytes    = "By"
	UnitRequests = "{request}"
)

type instruments struct {
	// https://opentelemetry.io/docs/specs/semconv/http/http-metrics/#metric-httpserverrequestduration
	httpServerRequestDuration metric.Float64Histogram
	// https://opentelemetry.io/docs/specs/semconv/http/http-metrics/#metric-httpserveractive_requests
	httpServerActiveRequests metric.Int64UpDownCounter
	// https://opentelemetry.io/docs/specs/semconv/http/http-metrics/#metric-httpserverrequestbodysize
	httpServerRequestBodySize metric.Int64Histogram
	// https://opentelemetry.io/docs/specs/semconv/http/http-metrics/#metric-httpserverresponsebodysize
	httpServerResponseBodySize metric.Int64Histogram
}

type middleware struct {
	config      config
	tracer      oteltrace.Tracer
	instruments instruments
	attributes  allAttrs
}

// Middleware returns fiber handler which will instrument incoming requests.
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
	if cfg.MeterProvider == nil {
		cfg.MeterProvider = otel.GetMeterProvider()
	}
	if cfg.Propagators == nil {
		cfg.Propagators = otel.GetTextMapPropagator()
	}
	if cfg.SpanNameFormatter == nil {
		cfg.SpanNameFormatter = defaultSpanNameFormatter
	}

	tracer := cfg.TracerProvider.Tracer(
		instrumentationName,
		oteltrace.WithInstrumentationVersion(otelcontrib.Version()),
	)

	meter := cfg.MeterProvider.Meter(
		instrumentationName,
		metric.WithInstrumentationVersion(otelcontrib.Version()),
	)

	instruments := initializeInstruments(meter)

	mw := &middleware{
		config:      cfg,
		tracer:      tracer,
		instruments: instruments,
	}

	return mw.handler
}

func initializeInstruments(meter metric.Meter) instruments {
	// https://opentelemetry.io/docs/specs/semconv/http/http-metrics/#metric-httpserverrequestduration
	httpServerRequestDuration, err := meter.Float64Histogram(
		MetricNameHttpServerRequestDuration,
		metric.WithUnit(UnitSeconds),
		metric.WithDescription("Duration of HTTP server requests."),
		metric.WithExplicitBucketBoundaries(
			0.005,
			0.01,
			0.025,
			0.05,
			0.075,
			0.1,
			0.25,
			0.5,
			0.75,
			1.0,
			2.5,
			5.0,
			7.5,
			10.0,
		),
	)
	if err != nil {
		otel.Handle(err)
	}

	// https://opentelemetry.io/docs/specs/semconv/http/http-metrics/#metric-httpserveractive_requests
	httpServerActiveRequests, err := meter.Int64UpDownCounter(
		MetricNameHttpServerActiveRequests,
		metric.WithUnit(UnitRequests),
		metric.WithDescription("Number of active HTTP server requests."),
	)
	if err != nil {
		otel.Handle(err)
	}

	// https://opentelemetry.io/docs/specs/semconv/http/http-metrics/#metric-httpserverrequestbodysize
	httpServerRequestBodySize, err := meter.Int64Histogram(
		MetricNameHttpServerRequestBodySize,
		metric.WithUnit(UnitBytes),
		metric.WithDescription("Size of HTTP server request bodies."),
	)
	if err != nil {
		otel.Handle(err)
	}

	// https://opentelemetry.io/docs/specs/semconv/http/http-metrics/#metric-httpserverresponsebodysize
	httpServerResponseBodySize, err := meter.Int64Histogram(
		MetricNameHttpServerResponseBodySize,
		metric.WithUnit(UnitBytes),
		metric.WithDescription("Size of HTTP server response bodies."),
	)
	if err != nil {
		otel.Handle(err)
	}

	return instruments{
		httpServerRequestDuration:  httpServerRequestDuration,
		httpServerActiveRequests:   httpServerActiveRequests,
		httpServerRequestBodySize:  httpServerRequestBodySize,
		httpServerResponseBodySize: httpServerResponseBodySize,
	}
}

// handler is the actual middleware handler
func (mw *middleware) handler(c fiber.Ctx) error {
	if mw.config.Next != nil && mw.config.Next(c) {
		return c.Next()
	}

	c.Locals(tracerKey, mw.tracer)
	savedCtx := c
	start := time.Now()

	requestBodySize := mw.buildRequestAttributes(c)

	mw.instruments.httpServerActiveRequests.Add(
		savedCtx,
		1,
		metric.WithAttributes(mw.attributes.LowCardinalitySlice()...),
	)

	ctx := mw.extractTracingContext(c, savedCtx)

	spanName := mw.config.SpanNameFormatter(c)

	ctx, span := mw.tracer.Start(ctx, spanName,
		oteltrace.WithSpanKind(oteltrace.SpanKindServer),
		oteltrace.WithAttributes(mw.attributes.ToSlice()...),
	)

	c.SetUserContext(ctx)

	err := c.Next()
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}

	statusCode := c.Response().StatusCode()

	responseBodySize := mw.buildResponseAttributes(c, statusCode)

	attrs := mw.attributes.LowCardinalitySlice()

	if mw.config.CustomAttributes != nil {
		attrs = append(attrs, mw.config.CustomAttributes(savedCtx)...)
	}

	mw.recordMetrics(savedCtx, start, requestBodySize, responseBodySize, attrs)

	mw.finalizeSpan(c, span, statusCode, attrs)

	mw.injectTracingHeaders(c, ctx)

	span.End()

	mw.instruments.httpServerActiveRequests.Add(
		savedCtx,
		-1,
		metric.WithAttributes(mw.attributes.ToSlice()...),
	)

	return err
}

func (mw *middleware) buildRequestAttributes(c fiber.Ctx) int64 {
	mw.attributes.lowCardinality = getLowCardinalityAttrsFromRequest(c, mw.config)
	requestSize := int64(0)
	mw.attributes.highCardinality, requestSize = getHighCardinalityAttrsFromRequest(c, mw.config)
	return requestSize
}

func (mw *middleware) buildResponseAttributes(c fiber.Ctx, statusCode int) int64 {
	mw.attributes.lowCardinality.HTTPResponseStatusCode = semconv.HTTPResponseStatusCode(
		statusCode,
	)

	responseSize := int64(0)
	if c.GetRespHeader("Content-Type") != "text/event-stream" {
		responseSize = int64(len(c.Response().Body()))
		mw.attributes.highCardinality.HTTPResponseBodySize = semconv.HTTPResponseBodySize(
			int(responseSize),
		)
	}

	// This overrides HTTPRoute from request
	mw.attributes.lowCardinality.HTTPRoute = semconv.HTTPRoute(c.Route().Path)

	return responseSize
}

func (mw *middleware) extractTracingContext(c fiber.Ctx, savedCtx context.Context) context.Context {
	reqHeader := make(http.Header)
	c.Request().Header.VisitAll(func(k, v []byte) {
		reqHeader.Add(utils.UnsafeString(k), utils.UnsafeString(v))
	})
	return mw.config.Propagators.Extract(savedCtx, propagation.HeaderCarrier(reqHeader))
}

func (mw *middleware) recordMetrics(
	c fiber.Ctx,
	start time.Time,
	requestBodySize int64,
	responseBodySize int64,
	attrs []attribute.KeyValue,
) {
	if mw.config.CustomMetricsAttributes != nil {
		attrs = append(attrs, mw.config.CustomMetricsAttributes(c)...)
	}

	duration := time.Since(start).Seconds()

	mw.instruments.httpServerRequestDuration.Record(
		c,
		duration,
		metric.WithAttributes(attrs...),
	)
	mw.instruments.httpServerRequestBodySize.Record(
		c,
		requestBodySize,
		metric.WithAttributes(attrs...),
	)
	mw.instruments.httpServerResponseBodySize.Record(
		c,
		responseBodySize,
		metric.WithAttributes(attrs...),
	)
}

func (mw *middleware) finalizeSpan(
	c fiber.Ctx,
	span oteltrace.Span,
	statusCode int,
	attrs []attribute.KeyValue,
) {
	if mw.config.CustomTracesAttributes != nil {
		attrs = append(attrs, mw.config.CustomTracesAttributes(c)...)
	}

	span.SetAttributes(attrs...)

	if statusCode >= 400 {
		span.SetStatus(codes.Error, http.StatusText(statusCode))
	} else {
		span.SetStatus(codes.Ok, "")
	}
}

func (mw *middleware) injectTracingHeaders(c fiber.Ctx, ctx context.Context) {
	tracingHeaders := make(propagation.HeaderCarrier)
	mw.config.Propagators.Inject(ctx, tracingHeaders)
	for _, headerKey := range tracingHeaders.Keys() {
		c.Set(headerKey, tracingHeaders.Get(headerKey))
	}
}

// defaultSpanNameFormatter is the default formatter for spans created with the fiber
// integration. It follows [OpenTelemetry guidelines]
//
// [OpenTelemetry guidelines]: https://opentelemetry.io/docs/specs/semconv/http/http-spans/#name
func defaultSpanNameFormatter(c fiber.Ctx) string {
	method := utils.CopyString(c.Method())
	path := utils.CopyString(c.Route().Path)

	// Should never happen
	if method == "" && path == "" {
		return "_OTHER"
	}

	if method != "" && path != "" {
		return method + " " + path
	}

	return method + path
}
