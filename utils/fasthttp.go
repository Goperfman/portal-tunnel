package utils

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/valyala/fasthttp"
)

var errFastHTTPUnsupported = errors.New("fasthttp unsupported")

var fastHTTPClient = &fasthttp.Client{
	Name:                          "portal-tunnel",
	MaxConnsPerHost:               1024,
	ReadTimeout:                   30 * time.Second,
	WriteTimeout:                  30 * time.Second,
	MaxIdleConnDuration:           90 * time.Second,
	NoDefaultUserAgentHeader:      true,
	DisableHeaderNamesNormalizing: true,
}

var (
	fastHTTPEnabledOnce sync.Once
	fastHTTPEnabledVal  bool
)

func fastHTTPEnabled() bool {
	fastHTTPEnabledOnce.Do(func() {
		fastHTTPEnabledVal = true
		if raw := os.Getenv("PORTAL_FASTHTTP"); raw != "" {
			if enabled, err := strconv.ParseBool(raw); err == nil {
				fastHTTPEnabledVal = enabled
			}
		}
	})
	return fastHTTPEnabledVal
}

func fastHTTPDoJSON(ctx context.Context, method, rawURL string, payload any, headers http.Header, out any) error {
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		return errFastHTTPUnsupported
	}

	reqHeaders := make(http.Header, len(headers))
	for key, values := range headers {
		reqHeaders[key] = append([]string(nil), values...)
	}

	var body []byte
	if payload != nil {
		var err error
		marshalCtx, releaseAllocator := fastHTTPAllocatorContext(ctx)
		defer releaseAllocator()
		body, err = MarshalJSON(marshalCtx, payload)
		if err != nil {
			return fmt.Errorf("marshal payload: %w", err)
		}
		if reqHeaders.Get("Content-Type") == "" {
			reqHeaders.Set("Content-Type", "application/json")
		}
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(rawURL)
	req.Header.SetMethod(method)
	for key, values := range reqHeaders {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	if len(body) > 0 {
		req.SetBodyRaw(body)
	}

	timeout := 30 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return context.DeadlineExceeded
		}
		timeout = remaining
	}
	if err := fastHTTPClient.DoTimeout(req, resp, timeout); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(resp.Body(), out); err != nil {
		return err
	}
	return nil
}
