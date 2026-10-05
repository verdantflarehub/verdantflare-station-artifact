// Package mcprpc handles bounded JSON-RPC envelopes without changing tool data.
package mcprpc

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"unicode/utf8"
)

const MaxBytes = 5 << 20

var ErrInvalid = errors.New("invalid JSON-RPC request")

type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

func Decode(r io.Reader) (Request, error) {
	var request Request
	b, e := io.ReadAll(io.LimitReader(r, MaxBytes+1))
	if e != nil || len(b) > MaxBytes || !utf8.Valid(b) {
		return request, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if unique(d, 0) != nil {
		return request, ErrInvalid
	}
	if _, e = d.Token(); e != io.EOF {
		return request, ErrInvalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(b, &fields) != nil || fields == nil {
		return request, ErrInvalid
	}
	for k := range fields {
		if k != "jsonrpc" && k != "id" && k != "method" && k != "params" {
			return request, ErrInvalid
		}
	}
	if json.Unmarshal(b, &request) != nil || request.JSONRPC != "2.0" || request.Method == "" {
		return request, ErrInvalid
	}
	if raw, ok := fields["params"]; ok {
		var object map[string]json.RawMessage
		if json.Unmarshal(raw, &object) != nil || object == nil {
			return request, ErrInvalid
		}
	}
	if len(request.ID) > 0 {
		if bytes.Equal(bytes.TrimSpace(request.ID), []byte("null")) {
			return request, ErrInvalid
		}
		var s string
		if json.Unmarshal(request.ID, &s) == nil {
			if len(s) > 128 {
				return request, ErrInvalid
			}
		} else {
			n, e := strconv.ParseInt(string(request.ID), 10, 64)
			if e != nil || n > 9007199254740991 || n < -9007199254740991 {
				return request, ErrInvalid
			}
		}
	}
	return request, nil
}
func unique(d *json.Decoder, depth int) error {
	if depth > 64 {
		return ErrInvalid
	}
	t, e := d.Token()
	if e != nil {
		return e
	}
	switch t {
	case json.Delim('{'):
		seen := map[string]bool{}
		for d.More() {
			k, e := d.Token()
			if e != nil {
				return e
			}
			key, ok := k.(string)
			if !ok || seen[key] {
				return ErrInvalid
			}
			seen[key] = true
			if e = unique(d, depth+1); e != nil {
				return e
			}
		}
		t, e = d.Token()
		if e != nil || t != json.Delim('}') {
			return ErrInvalid
		}
	case json.Delim('['):
		for d.More() {
			if e = unique(d, depth+1); e != nil {
				return e
			}
		}
		t, e = d.Token()
		if e != nil || t != json.Delim(']') {
			return ErrInvalid
		}
	default:
		if _, ok := t.(json.Delim); ok {
			return ErrInvalid
		}
	}
	return nil
}

func (r Request) Call() (string, map[string]any, json.RawMessage, error) {
	var params map[string]json.RawMessage
	if len(r.ID) == 0 || json.Unmarshal(r.Params, &params) != nil {
		return "", nil, nil, ErrInvalid
	}
	for k := range params {
		if k != "name" && k != "arguments" && k != "_meta" {
			return "", nil, nil, ErrInvalid
		}
	}
	var name string
	if json.Unmarshal(params["name"], &name) != nil || name == "" {
		return "", nil, nil, ErrInvalid
	}
	raw := params["arguments"]
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var args map[string]any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(&args) != nil || args == nil {
		return "", nil, nil, ErrInvalid
	}
	return name, args, raw, nil
}
func Result(w http.ResponseWriter, r Request, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": r.ID, "result": result})
}
func Error(w http.ResponseWriter, r Request, status, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": r.ID, "error": map[string]any{"code": code, "message": message}})
}

// Reject consumes a bounded small body before an early authentication/protocol
// rejection. HTTP/1 clients may request Connection: close and send headers and
// body separately; closing with unread bytes can discard the reply via TCP RST
// on Windows. Server ReadTimeout still bounds slow bodies. Large or streaming
// unauthenticated uploads are not drained.
func Reject(w http.ResponseWriter, incoming *http.Request, r Request, status, code int, message string) {
	if incoming.Body != nil && incoming.ContentLength > 0 && incoming.ContentLength <= 64<<10 && incoming.Header.Get("Expect") == "" {
		_, _ = io.Copy(io.Discard, io.LimitReader(incoming.Body, incoming.ContentLength))
	}
	Error(w, r, status, code, message)
}
func Common(w http.ResponseWriter, r Request, name, version string) bool {
	switch r.Method {
	case "initialize":
		if len(r.ID) == 0 {
			Error(w, r, 400, -32600, "Request ID required")
			return true
		}
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if json.Unmarshal(r.Params, &params) != nil || params.ProtocolVersion == "" {
			Error(w, r, 400, -32602, "Invalid initialize parameters")
			return true
		}
		v := params.ProtocolVersion
		if v != "2025-03-26" && v != "2025-06-18" {
			v = "2025-06-18"
		}
		Result(w, r, map[string]any{"protocolVersion": v, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": name, "version": version}})
	case "notifications/initialized":
		if len(r.ID) != 0 {
			Error(w, r, 400, -32600, "Notification must not have an ID")
		} else {
			w.WriteHeader(http.StatusAccepted)
		}
	case "ping":
		if len(r.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
		} else {
			Result(w, r, map[string]any{})
		}
	default:
		return false
	}
	return true
}
