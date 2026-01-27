package vsphere

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"time"

	"k8s.io/klog/v2"
)

// SOAPLogEntry represents a SOAP API call log entry
type SOAPLogEntry struct {
	Timestamp    time.Time
	Method       string
	RequestBody  string
	ResponseBody string
	Duration     time.Duration
	Error        error
}

// RESTLogEntry represents a REST API call log entry
type RESTLogEntry struct {
	Timestamp      time.Time
	Method         string
	URL            string
	RequestBody    string
	ResponseBody   string
	ResponseStatus int
	Duration       time.Duration
	Error          error
}

// SOAPLogger logs SOAP calls
type SOAPLogger struct {
	entries []SOAPLogEntry
}

// NewSOAPLogger creates a new SOAP logger
func NewSOAPLogger() *SOAPLogger {
	return &SOAPLogger{
		entries: make([]SOAPLogEntry, 0),
	}
}

// LogSOAPCall logs a SOAP API call
func (l *SOAPLogger) LogSOAPCall(ctx context.Context, method string, req, res interface{}, duration time.Duration, err error) {
	// Marshal request and response for logging
	reqBody := l.marshalSOAPBody(req)
	resBody := l.marshalSOAPBody(res)

	// If method is empty, extract from request
	if method == "" {
		method = l.extractSOAPMethod(req)
	}

	entry := SOAPLogEntry{
		Timestamp:    time.Now().Add(-duration),
		Method:       method,
		RequestBody:  reqBody,
		ResponseBody: resBody,
		Duration:     duration,
		Error:        err,
	}

	l.entries = append(l.entries, entry)

	// Log to klog
	logger := klog.FromContext(ctx)
	if err != nil {
		logger.Error(err, "SOAP call failed",
			"method", method,
			"duration", duration)
	} else {
		logger.V(2).Info("SOAP call succeeded",
			"method", method,
			"duration", duration)
	}

	// Log full request/response at V(4)
	logger.V(4).Info("SOAP details",
		"method", method,
		"request", reqBody,
		"response", resBody)
}

// marshalSOAPBody marshals a SOAP body to string
func (l *SOAPLogger) marshalSOAPBody(body interface{}) string {
	if body == nil {
		return ""
	}

	data, err := xml.MarshalIndent(body, "", "  ")
	if err != nil {
		return fmt.Sprintf("error marshaling: %v", err)
	}

	return string(data)
}

// extractSOAPMethod extracts the method name from a SOAP request
func (l *SOAPLogger) extractSOAPMethod(req interface{}) string {
	// Try to get type name
	return fmt.Sprintf("%T", req)
}

// GetEntries returns all logged entries
func (l *SOAPLogger) GetEntries() []SOAPLogEntry {
	return l.entries
}

// Clear clears all logged entries
func (l *SOAPLogger) Clear() {
	l.entries = make([]SOAPLogEntry, 0)
}

// RESTLogger logs REST API calls
type RESTLogger struct {
	entries []RESTLogEntry
}

// NewRESTLogger creates a new REST logger
func NewRESTLogger() *RESTLogger {
	return &RESTLogger{
		entries: make([]RESTLogEntry, 0),
	}
}

// RoundTrip implements http.RoundTripper
func (l *RESTLogger) RoundTrip(rt http.RoundTripper) http.RoundTripper {
	return &restLoggerTransport{
		base:   rt,
		logger: l,
	}
}

type restLoggerTransport struct {
	base   http.RoundTripper
	logger *RESTLogger
}

func (t *restLoggerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()

	// Read request body if present
	var reqBody string
	if req.Body != nil {
		bodyBytes, err := io.ReadAll(req.Body)
		if err == nil {
			reqBody = string(bodyBytes)
			// Restore body for actual request
			req.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
		}
	}

	// Execute the actual REST call
	res, err := t.base.RoundTrip(req)

	duration := time.Since(start)

	// Read response body if present
	var resBody string
	var statusCode int
	if res != nil {
		statusCode = res.StatusCode
		if res.Body != nil {
			bodyBytes, readErr := io.ReadAll(res.Body)
			if readErr == nil {
				resBody = string(bodyBytes)
				// Restore body for caller
				res.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
			}
		}
	}

	entry := RESTLogEntry{
		Timestamp:      start,
		Method:         req.Method,
		URL:            req.URL.String(),
		RequestBody:    reqBody,
		ResponseBody:   resBody,
		ResponseStatus: statusCode,
		Duration:       duration,
		Error:          err,
	}

	t.logger.entries = append(t.logger.entries, entry)

	// Log to klog
	ctx := req.Context()
	logger := klog.FromContext(ctx)

	if err != nil {
		logger.Error(err, "REST call failed",
			"method", req.Method,
			"url", req.URL.String(),
			"duration", duration)
	} else {
		logger.V(2).Info("REST call succeeded",
			"method", req.Method,
			"url", req.URL.String(),
			"status", statusCode,
			"duration", duration)
	}

	// Log full request/response at V(4)
	logger.V(4).Info("REST details",
		"method", req.Method,
		"url", req.URL.String(),
		"request", reqBody,
		"response", resBody)

	return res, err
}

// GetEntries returns all logged entries
func (l *RESTLogger) GetEntries() []RESTLogEntry {
	return l.entries
}

// Clear clears all logged entries
func (l *RESTLogger) Clear() {
	l.entries = make([]RESTLogEntry, 0)
}
