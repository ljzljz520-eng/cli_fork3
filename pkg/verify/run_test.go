// Copyright 2023 Vic Shóstak and Create Go App Contributors. All rights reserved.
// Use of this source code is governed by Apache 2.0 license
// that can be found in the LICENSE file.

package verify

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestProbeHTTPStatusAndHostHeader(t *testing.T) {
	var gotHost string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		if r.URL.Path == "/hc/status" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	ok, detail := probeHTTP(Probe{
		Kind: ProbeHTTP, URL: server.URL + "/hc/status",
		Host: "example.com", ExpectStatus: 200,
	})
	if !ok {
		t.Fatalf("health probe failed: %s", detail)
	}
	if gotHost != "example.com" {
		t.Errorf("Host header not forwarded: %q", gotHost)
	}

	ok, _ = probeHTTP(Probe{Kind: ProbeHTTP, URL: server.URL + "/missing", ExpectStatus: 200})
	if ok {
		t.Error("probe must fail on status mismatch")
	}
}

func TestProbeCORS(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") == "http://localhost:5173" {
			w.Header().Set("Access-Control-Allow-Origin", r.Header.Get("Origin"))
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ok, detail := probeCORS(Probe{
		Kind: ProbeCORS, URL: server.URL, Origin: "http://localhost:5173", ExpectStatus: 200,
	})
	if !ok {
		t.Fatalf("cors probe failed: %s", detail)
	}

	ok, _ = probeCORS(Probe{Kind: ProbeCORS, URL: server.URL, ExpectStatus: 200})
	if ok {
		t.Error("CORS probe without Origin must fail")
	}

	ok, _ = probeCORS(Probe{
		Kind: ProbeCORS, URL: server.URL, Origin: "http://localhost:9999", ExpectStatus: 200,
		ExpectHeader: "Access-Control-Allow-Origin",
	})
	if ok {
		t.Error("CORS probe must fail when the server omits the header")
	}
}

func TestProbeTCP(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	ok, detail := probeTCP(Probe{Kind: ProbeTCP, URL: listener.Addr().String()})
	if !ok {
		t.Fatalf("tcp probe failed: %s", detail)
	}

	ok, _ = probeTCP(Probe{Kind: ProbeTCP, URL: "127.0.0.1:1"})
	if ok {
		t.Error("tcp probe to closed port must fail")
	}
}

func TestRunNilPlan(t *testing.T) {
	if _, _, err := Run(context.Background(), nil); err == nil {
		t.Error("nil plan must error")
	}
}

// TestRunDockerIntegration exercises the full Compose lifecycle. It is gated
// behind CGAPP_TEST_DOCKER=1 because it requires Docker and image pulls.
func TestRunDockerIntegration(t *testing.T) {
	if os.Getenv("CGAPP_TEST_DOCKER") != "1" {
		t.Skip("set CGAPP_TEST_DOCKER=1 to run Docker integration tests")
	}

	dir := t.TempDir()
	compose := "services:\n" +
		"  app:\n" +
		"    image: traefik/whoami:v1.10\n" +
		"    ports:\n" +
		"      - \"8080:80\"\n"
	if err := os.WriteFile(dir+"/docker-compose.yml", []byte(compose), 0o600); err != nil {
		t.Fatal(err)
	}

	plan := &Plan{
		WorkDir:      dir,
		ComposeFile:  "docker-compose.yml",
		ProjectName:  "cgapp_verify_it",
		AppServices:  []string{"app"},
		AllServices:  []string{"app"},
		Probes:       []Probe{{Name: "app-http", Kind: ProbeHTTP, URL: "http://127.0.0.1:8080/", ExpectStatus: 200}},
		BuildTimeout: 5 * time.Minute,
		WaitTimeout:  2 * time.Minute,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	results, logs, err := Run(ctx, plan)
	if err != nil {
		t.Fatalf("verify run failed: %v\nlogs:\n%s", err, logs)
	}
	if len(results) != 1 || !results[0].OK {
		t.Fatalf("unexpected probe results: %+v", results)
	}
}
