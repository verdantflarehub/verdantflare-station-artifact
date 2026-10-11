package main

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func artifactListenerConfig(business, mcp, advertise, endpoints, mode string, withTLS bool) error {
	if mcp == "" {
		if mode == "split" && advertise != "" {
			return errors.New("split Artifact MCP registration requires a separate listener")
		}
		return nil
	}
	_, businessPort, err := net.SplitHostPort(business)
	if err != nil {
		return errors.New("invalid Artifact business listener")
	}
	_, port, err := net.SplitHostPort(mcp)
	n, numberErr := strconv.Atoi(port)
	if err != nil || numberErr != nil || n < 1 || n > 65535 || port != strconv.Itoa(n) || port == businessPort {
		return errors.New("Artifact MCP requires a distinct fixed port")
	}
	if !withTLS && nonLoopbackListen(mcp) {
		return errors.New("Artifact MCP non-loopback listener requires TLS")
	}
	u, err := url.Parse(advertise)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "/mcp" || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		strings.ContainsAny(advertise, "\\ \t\r\n") || (u.Scheme != "https" && u.Scheme != "http") || (u.Scheme == "https") != withTLS || strings.TrimSpace(endpoints) == "" {
		return errors.New("Artifact MCP listener requires matching advertisement and registry")
	}
	return nil
}

func artifactListenerHandler(next, health http.Handler, mcpOnly, denyMCP bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mcpOnly {
			if r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.ForceQuery || (r.URL.Path != "/mcp" && r.URL.Path != "/health") {
				http.NotFound(w, r)
				return
			}
		} else if denyMCP && (r.URL.Path == "/mcp" || strings.HasPrefix(r.URL.Path, "/mcp/")) {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == "/health" {
			if r.Method != http.MethodGet {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			health.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}
