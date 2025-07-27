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

type lowCardinalityAttrs struct {
	HTTPMethod             attribute.KeyValue
	HTTPRoute              attribute.KeyValue
	HTTPResponseStatusCode attribute.KeyValue
	ServerAddress          attribute.KeyValue
	ServerPort             attribute.KeyValue
	NetworkTransport       attribute.KeyValue
	NetworkProtocolName    attribute.KeyValue
	NetworkProtocolVersion attribute.KeyValue
	URLScheme              attribute.KeyValue
}

type highCardinalityAttrs struct {
	URLPath              attribute.KeyValue
	HTTPRequestBodySize  attribute.KeyValue
	HTTPResponseBodySize attribute.KeyValue
	UserAgentOriginal    attribute.KeyValue
	EnduserID            attribute.KeyValue
	ClientAddress        attribute.KeyValue
	ClientPort           attribute.KeyValue
}

type allAttrs struct {
	lowCardinality  lowCardinalityAttrs
	highCardinality highCardinalityAttrs
}

func (attr allAttrs) ToSlice() []attribute.KeyValue {
	result := make([]attribute.KeyValue, 0, 16)
	result = append(result, attr.LowCardinalitySlice()...)
	result = append(result, attr.HighCardinalitySlice()...)
	return result
}

func (attr allAttrs) LowCardinalitySlice() []attribute.KeyValue {
	result := make([]attribute.KeyValue, 0, 9)

	if attr.lowCardinality.HTTPMethod.Key != "" {
		result = append(result, attr.lowCardinality.HTTPMethod)
	}
	if attr.lowCardinality.HTTPRoute.Key != "" {
		result = append(result, attr.lowCardinality.HTTPRoute)
	}
	if attr.lowCardinality.HTTPResponseStatusCode.Key != "" {
		result = append(result, attr.lowCardinality.HTTPResponseStatusCode)
	}
	if attr.lowCardinality.ServerAddress.Key != "" {
		result = append(result, attr.lowCardinality.ServerAddress)
	}
	if attr.lowCardinality.ServerPort.Key != "" {
		result = append(result, attr.lowCardinality.ServerPort)
	}
	if attr.lowCardinality.NetworkTransport.Key != "" {
		result = append(result, attr.lowCardinality.NetworkTransport)
	}
	if attr.lowCardinality.NetworkProtocolName.Key != "" {
		result = append(result, attr.lowCardinality.NetworkProtocolName)
	}
	if attr.lowCardinality.NetworkProtocolVersion.Key != "" {
		result = append(result, attr.lowCardinality.NetworkProtocolVersion)
	}
	if attr.lowCardinality.URLScheme.Key != "" {
		result = append(result, attr.lowCardinality.URLScheme)
	}

	return result
}

func (attr allAttrs) HighCardinalitySlice() []attribute.KeyValue {
	result := make([]attribute.KeyValue, 0, 7)

	if attr.highCardinality.URLPath.Key != "" {
		result = append(result, attr.highCardinality.URLPath)
	}
	if attr.highCardinality.HTTPRequestBodySize.Key != "" {
		result = append(result, attr.highCardinality.HTTPRequestBodySize)
	}
	if attr.highCardinality.HTTPResponseBodySize.Key != "" {
		result = append(result, attr.highCardinality.HTTPResponseBodySize)
	}
	if attr.highCardinality.UserAgentOriginal.Key != "" {
		result = append(result, attr.highCardinality.UserAgentOriginal)
	}
	if attr.highCardinality.EnduserID.Key != "" {
		result = append(result, attr.highCardinality.EnduserID)
	}
	if attr.highCardinality.ClientAddress.Key != "" {
		result = append(result, attr.highCardinality.ClientAddress)
	}
	if attr.highCardinality.ClientPort.Key != "" {
		result = append(result, attr.highCardinality.ClientPort)
	}

	return result
}

func getLowCardinalityAttrsFromRequest(c fiber.Ctx, cfg config) lowCardinalityAttrs {
	attrs := lowCardinalityAttrs{}

	attrs.HTTPMethod = translateHTTPMethodToSemconv(utils.CopyString(c.Method()))
	attrs.HTTPRoute = semconv.HTTPRoute(utils.CopyString(c.Route().Path))
	attrs.ServerAddress = semconv.ServerAddress(utils.CopyString(c.Hostname()))
	attrs.ServerPort = func() attribute.KeyValue {
		if cfg.Port != nil {
			return semconv.ServerPort(*cfg.Port)
		}
		return attribute.KeyValue{}
	}()
	attrs.NetworkTransport = semconv.NetworkTransportTCP

	protoc := strings.Split(utils.CopyString(c.Protocol()), "/")
	if len(protoc) == 2 {
		protocName := strings.ToLower(protoc[0])
		attrs.URLScheme = semconv.URLScheme(protocName)
		attrs.NetworkProtocolName = semconv.NetworkProtocolName(protocName)
		attrs.NetworkProtocolVersion = semconv.NetworkProtocolVersion(protoc[1])
	}

	return attrs
}

func getHighCardinalityAttrsFromRequest(c fiber.Ctx, cfg config) (highCardinalityAttrs, int64) {
	attrs := highCardinalityAttrs{}

	userAgent := utils.CopyBytes(c.Request().Header.UserAgent())

	attrs.URLPath = semconv.URLPath(string(utils.CopyBytes(c.Request().URI().Path())))
	attrs.UserAgentOriginal = semconv.UserAgentOriginal(string(userAgent))

	requestBodySize := int64(0)
	if c.Get("Content-Type") != "text/event-stream" {
		head := utils.CopyString(c.Get(fiber.HeaderContentLength))
		size, err := strconv.Atoi(head)
		// Ignore body size calculation if convertion fails
		if err == nil {
			requestBodySize = int64(size)
			attrs.HTTPRequestBodySize = semconv.HTTPRequestBodySize(size)
		}
	}

	if username, ok := HasBasicAuth(utils.CopyString(c.Get(fiber.HeaderAuthorization))); ok {
		attrs.EnduserID = semconv.EnduserID(utils.CopyString(username))
	}

	if cfg.collectClientIP {
		clientIP := c.IP()
		if len(clientIP) > 0 {
			attrs.ClientAddress = semconv.ClientAddress(utils.CopyString(clientIP))
		}

		if clientPort, err := strconv.Atoi(utils.CopyString(c.Port())); err == nil {
			attrs.ClientPort = semconv.ClientPort(clientPort)
		}
	}

	return attrs, requestBodySize
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
