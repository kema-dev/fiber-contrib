package otelfiber

import (
	"fmt"
	"net/http"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// SpanStatusFromHTTPStatusCodeAndSpanKind generates a status code and a message
// as specified by the OpenTelemetry specification for a span.
// Exclude 4xx for SERVER to set the appropriate status.
// See [OpenTelemetry documentation on Span Status]
//
// [OpenTelemetry documentation on Span Status]: https://opentelemetry.io/docs/concepts/signals/traces/#span-status
func SpanStatusFromHTTPStatusCodeAndSpanKind(
	code int,
	spanKind trace.SpanKind,
) (codes.Code, string) {
	// This code block ignores the HTTP 306 status code. The 306 status code is no longer in use.
	statusText := http.StatusText(code)
	if statusText == "" {
		return codes.Error, fmt.Sprintf("invalid http status code %d", code)
	}

	if (code >= http.StatusContinue && code < http.StatusBadRequest) ||
		(spanKind == trace.SpanKindServer && (code >= 400 && code < 500)) {
		return codes.Unset, ""
	}
	return codes.Error, statusText
}
