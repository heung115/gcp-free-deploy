package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNativeHTTPGet(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusFound, http.StatusBadRequest, http.StatusInternalServerError} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "http://127.0.0.1:1/should-not-follow")
				w.WriteHeader(status)
				fmt.Fprint(w, "private-response")
			}))
			defer server.Close()
			body, err := (ExecRunner{}).HTTPGet(context.Background(), server.URL)
			if status < 400 {
				if err != nil || body != "private-response" {
					t.Fatalf("HTTPGet() = %q, %v", body, err)
				}
			} else if err == nil || strings.Contains(err.Error(), "private-response") || strings.Contains(err.Error(), server.URL) {
				t.Fatalf("unexpected error = %v", err)
			}
		})
	}
}

func TestNativeHTTPGetLimitsBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, strings.Repeat("x", localHTTPBodyLimit*3))
	}))
	defer server.Close()
	body, err := (ExecRunner{}).HTTPGet(context.Background(), server.URL)
	if err != nil || len(body) != localHTTPBodyLimit {
		t.Fatalf("body length = %d, error = %v", len(body), err)
	}
}

func TestNativeHTTPGetRespectsContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (ExecRunner{}).HTTPGet(ctx, server.URL); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := (ExecRunner{}).HTTPGet(ctx, server.URL); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
}

func TestNativeHTTPGetUsesIPv4(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil || net.ParseIP(host).To4() == nil {
			t.Errorf("request address = %q", r.RemoteAddr)
		}
		fmt.Fprint(w, "IPv4")
	}))
	defer server.Close()
	// localhost can resolve to both address families; the connection must use IPv4.
	url := strings.Replace(server.URL, "127.0.0.1", "localhost", 1)
	body, err := (ExecRunner{}).HTTPGet(context.Background(), url)
	if err != nil || body != "IPv4" {
		t.Fatalf("HTTPGet() = %q, %v", body, err)
	}
}

func TestRunnerHTTPGetRequiresExplicitCapability(t *testing.T) {
	if _, err := runnerHTTPGet(context.Background(), &statusDeadlineRunner{}, "http://127.0.0.1:1"); err == nil {
		t.Fatal("runner without HTTP capability accepted")
	}
}
