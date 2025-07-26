package otelfiber

import (
	"encoding/base64"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/utils/v2"
	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.34.0"
)

func getLowCardinalityAttrsFromRequest(c fiber.Ctx, cfg config) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		translateHTTPMethodToSemconv(utils.CopyString(c.Method())),
		semconv.ServerAddress(utils.CopyString(c.Hostname())),
		semconv.HTTPRoute(utils.CopyString(c.Route().Path)),
		semconv.NetworkTransportTCP,
	}

	protocAttr := getProtocolAttrsFromRequest(c)
	if len(protocAttr) > 0 {
		attrs = append(attrs, protocAttr...)
	}

	if cfg.Port != nil {
		attrs = append(attrs, semconv.ServerPort(*cfg.Port))
	}

	return attrs
}

func getAllAttrsFromRequest(
	c fiber.Ctx,
	cfg config,
) []attribute.KeyValue {
	attrs := getLowCardinalityAttrsFromRequest(c, cfg)

	userAgent := utils.CopyBytes(c.Request().Header.UserAgent())

	attrs = append(
		attrs,
		semconv.URLPath(string(utils.CopyBytes(c.Request().URI().Path()))),
		semconv.URLOriginal(string(utils.CopyBytes(c.Request().URI().PathOriginal()))),
		semconv.UserAgentOriginal(string(userAgent)),
	)

	if username, ok := HasBasicAuth(utils.CopyString(c.Get(fiber.HeaderAuthorization))); ok {
		attrs = append(attrs, semconv.EnduserID(utils.CopyString(username)))
	}

	if cfg.collectClientIP {
		clientIP := c.IP()
		if len(clientIP) > 0 {
			attrs = append(attrs, semconv.ClientAddress(utils.CopyString(clientIP)))
		}

		if clientPort, err := strconv.Atoi(utils.CopyString(c.Port())); err == nil {
			attrs = append(attrs, semconv.ClientPort(clientPort))
		}
	}

	return attrs
}

func httpServerMetricAttributesFromRequest(c fiber.Ctx, cfg config) []attribute.KeyValue {
	attrs := getLowCardinalityAttrsFromRequest(c, cfg)

	if cfg.CustomMetricAttributes != nil {
		attrs = append(attrs, cfg.CustomMetricAttributes(c)...)
	}

	return attrs
}

func httpServerTraceAttributesFromRequest(c fiber.Ctx, cfg config) []attribute.KeyValue {
	attrs := getAllAttrsFromRequest(c, cfg)

	if cfg.CustomAttributes != nil {
		attrs = append(attrs, cfg.CustomAttributes(c)...)
	}

	return attrs
}

func getProtocolAttrsFromRequest(c fiber.Ctx) []attribute.KeyValue {
	protoc := strings.Split(utils.CopyString(c.Protocol()), "/")
	if len(protoc) != 2 {
		return nil
	}
	return []attribute.KeyValue{
		semconv.NetworkProtocolName(protoc[0]),
		semconv.URLScheme(protoc[0]),
		semconv.NetworkProtocolVersion(protoc[1]),
	}
}

func HasBasicAuth(auth string) (string, bool) {
	if auth == "" {
		return "", false
	}

	// Check if the Authorization header is Basic
	if !strings.HasPrefix(auth, "Basic ") {
		return "", false
	}

	// Decode the header contents
	raw, err := base64.StdEncoding.DecodeString(auth[6:])
	if err != nil {
		return "", false
	}

	// Get the credentials
	creds := string(raw)

	// Check if the credentials are in the correct form
	// which is "username:password".
	index := strings.Index(creds, ":")
	if index == -1 {
		return "", false
	}

	// Get the username
	return creds[:index], true
}

func translateHTTPMethodToSemconv(method string) attribute.KeyValue {
	switch method {
	case semconv.HTTPRequestMethodConnect.Value.AsString():
		return semconv.HTTPRequestMethodConnect
	case semconv.HTTPRequestMethodDelete.Value.AsString():
		return semconv.HTTPRequestMethodDelete
	case semconv.HTTPRequestMethodGet.Value.AsString():
		return semconv.HTTPRequestMethodGet
	case semconv.HTTPRequestMethodHead.Value.AsString():
		return semconv.HTTPRequestMethodHead
	case semconv.HTTPRequestMethodOptions.Value.AsString():
		return semconv.HTTPRequestMethodOptions
	case semconv.HTTPRequestMethodOther.Value.AsString():
		return semconv.HTTPRequestMethodOther
	case semconv.HTTPRequestMethodPatch.Value.AsString():
		return semconv.HTTPRequestMethodPatch
	case semconv.HTTPRequestMethodPost.Value.AsString():
		return semconv.HTTPRequestMethodPost
	case semconv.HTTPRequestMethodPut.Value.AsString():
		return semconv.HTTPRequestMethodPut
	case semconv.HTTPRequestMethodTrace.Value.AsString():
		return semconv.HTTPRequestMethodTrace
	default:
		return semconv.HTTPRequestMethodOther
	}
}
