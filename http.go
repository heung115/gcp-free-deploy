package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

const (
	localHTTPTimeout   = 10 * time.Second
	localHTTPBodyLimit = 4096
)

// HTTPRunner lets callers check HTTP endpoints without requiring a curl binary.
// Test runners implement it separately so tests never make unexpected requests.
type HTTPRunner interface {
	HTTPGet(context.Context, string) (string, error)
}

func runnerHTTPGet(ctx context.Context, runner Runner, url string) (string, error) {
	httpRunner, ok := runner.(HTTPRunner)
	if !ok {
		return "", errors.New("runner does not support HTTP requests")
	}
	return httpRunner.HTTPGet(ctx, url)
}

func (ExecRunner) HTTPGet(ctx context.Context, url string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, localHTTPTimeout)
	defer cancel()
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// The firewall accepts IPv4 CIDRs and the VM has an external IPv4 address.
	transport.DialContext = func(ctx context.Context, _, address string) (net.Conn, error) {
		return dialer.DialContext(ctx, "tcp4", address)
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport:     transport,
		Timeout:       localHTTPTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", errors.New("invalid HTTP endpoint")
	}
	response, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.New("HTTP request failed")
	}
	defer response.Body.Close()
	if response.StatusCode >= http.StatusBadRequest {
		return "", fmt.Errorf("HTTP endpoint returned status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, localHTTPBodyLimit))
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.New("HTTP response could not be read")
	}
	return string(body), nil
}
