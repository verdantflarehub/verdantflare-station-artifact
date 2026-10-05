package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/verdantflarehub/verdantflare-station-artifact/internal/artifact"
	"github.com/verdantflarehub/verdantflare-station-artifact/internal/mcprpc"
	"github.com/verdantflarehub/verdantflare-station-artifact/internal/strictjson"
)

const maxMCPText = 1 << 20

//go:embed mcp_schemas.json
var toolSchemas []byte

type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func MCPTools() []Tool {
	var tools []Tool
	if json.Unmarshal(toolSchemas, &tools) != nil {
		panic("invalid generated Artifact tool schema")
	}
	return tools
}

// Authentication remains in ServeHTTP. Tool methods call the same persistent
// service and authorizer as HTTP; MCP never supplies an allow-all authorizer.
func (s *Server) mcp(w http.ResponseWriter, r *http.Request) {
	var empty mcprpc.Request
	mt, _, err := mime.ParseMediaType(oneHeader(r, "Content-Type"))
	if r.Method != "POST" || err != nil || mt != "application/json" || r.URL.RawQuery != "" || r.Header.Get("Content-Encoding") != "" {
		mcprpc.Reject(w, r, empty, 400, -32600, "Invalid request")
		return
	}
	request, err := mcprpc.Decode(r.Body)
	if err != nil {
		mcprpc.Reject(w, r, empty, 400, -32700, "Invalid JSON-RPC request")
		return
	}
	if mcprpc.Common(w, request, "station-artifact", "0.1.0") {
		return
	}
	switch request.Method {
	case "tools/list":
		if len(request.ID) == 0 {
			mcprpc.Error(w, request, 400, -32600, "Request ID required")
			return
		}
		mcprpc.Result(w, request, map[string]any{"tools": MCPTools()})
	case "tools/call":
		name, _, args, err := request.Call()
		if err != nil {
			mcprpc.Error(w, request, 400, -32602, "Invalid tool parameters")
			return
		}
		var value any
		switch name {
		case "artifact.write":
			value, err = s.writeMCP(r.Context(), principal(r), args)
		case "artifact.read":
			value, err = s.readMCP(r.Context(), principal(r), args)
		default:
			mcprpc.Error(w, request, 404, -32601, "Unknown tool")
			return
		}
		if err != nil {
			_, code := errorStatus(err)
			value = map[string]string{"code": code, "message": "Artifact request failed", "request_id": principal(r).RequestID}
		}
		data, e := json.Marshal(value)
		if e != nil {
			mcprpc.Error(w, request, 500, -32603, "Response unavailable")
			return
		}
		mcprpc.Result(w, request, map[string]any{"content": []map[string]string{{"type": "text", "text": string(data)}}, "structuredContent": json.RawMessage(data), "isError": err != nil})
	default:
		mcprpc.Error(w, request, 404, -32601, "Method not found")
	}
}

func textMIME(raw string) bool {
	mt, params, err := mime.ParseMediaType(raw)
	return err == nil && (params["charset"] == "" || strings.EqualFold(params["charset"], "utf-8")) && (strings.HasPrefix(mt, "text/") || mt == "application/json" || strings.HasSuffix(mt, "+json"))
}
func userSource(s artifact.Source) bool { return s.Kind == "user_edit" || s.Kind == "user_import" }

func (s *Server) writeMCP(ctx context.Context, p artifact.Principal, args []byte) (any, error) {
	var operation struct {
		Mode string `json:"mode"`
	}
	if json.Unmarshal(args, &operation) != nil {
		return nil, artifact.ErrInvalid
	}
	decode := func(v any) error {
		if strictjson.Decode(bytes.NewReader(args), maxJSON, v) != nil {
			return artifact.ErrInvalid
		}
		return nil
	}
	switch operation.Mode {
	case "text":
		var in struct {
			Mode       string          `json:"mode"`
			WriteID    string          `json:"write_id"`
			ArtifactID string          `json:"artifact_id,omitempty"`
			Source     artifact.Source `json:"source"`
			MIME       string          `json:"mime"`
			Text       string          `json:"text"`
		}
		if decode(&in) != nil || !userSource(in.Source) || !textMIME(in.MIME) || len(in.Text) > maxMCPText || !utf8.ValidString(in.Text) {
			return nil, artifact.ErrInvalid
		}
		hash := sha256.Sum256([]byte(in.Text))
		u, err := s.service.Prepare(ctx, p, artifact.PrepareRequest{WriteID: in.WriteID, ArtifactID: in.ArtifactID, Source: in.Source, MIME: in.MIME, Size: int64(len(in.Text)), SHA256: hex.EncodeToString(hash[:])})
		if err != nil {
			return nil, err
		}
		if u.State != "committed" {
			if _, err = s.service.Put(ctx, p, u.UploadID, strings.NewReader(in.Text)); err != nil {
				return nil, err
			}
		}
		v, err := s.service.Commit(ctx, p, u.UploadID)
		return map[string]any{"version": v}, err
	case "prepare":
		var in struct {
			Mode string `json:"mode"`
			artifact.PrepareRequest
		}
		if decode(&in) != nil || !userSource(in.Source) {
			return nil, artifact.ErrInvalid
		}
		u, err := s.service.Prepare(ctx, p, in.PrepareRequest)
		return map[string]any{"upload": u}, err
	case "commit", "status":
		var in struct {
			Mode     string `json:"mode"`
			UploadID string `json:"upload_id"`
		}
		if decode(&in) != nil || !artifact.ValidID(in.UploadID) {
			return nil, artifact.ErrInvalid
		}
		u, err := s.service.Upload(ctx, p, in.UploadID)
		if err != nil {
			return nil, err
		}
		if !userSource(u.Source) {
			return nil, artifact.ErrForbidden
		}
		if in.Mode == "status" {
			return map[string]any{"upload": u}, nil
		}
		v, err := s.service.Commit(ctx, p, in.UploadID)
		return map[string]any{"version": v}, err
	}
	return nil, artifact.ErrInvalid
}

func (s *Server) readMCP(ctx context.Context, p artifact.Principal, args []byte) (any, error) {
	var in struct {
		Mode   string              `json:"mode"`
		Ref    artifact.ContentRef `json:"content_ref"`
		Access *artifact.Access    `json:"access,omitempty"`
	}
	if strictjson.Decode(bytes.NewReader(args), maxJSON, &in) != nil || !in.Ref.Valid() {
		return nil, artifact.ErrInvalid
	}
	a := artifact.Access{}
	if in.Access != nil {
		a = *in.Access
	}
	if !a.Valid() || (in.Mode != "metadata" && in.Mode != "text" && in.Mode != "download") {
		return nil, artifact.ErrInvalid
	}
	if in.Mode == "metadata" {
		v, err := s.service.Get(ctx, p, in.Ref, a)
		return map[string]any{"mode": in.Mode, "version": v}, err
	}
	v, f, err := s.service.Open(ctx, p, in.Ref, a)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if in.Mode == "text" {
		if v.Size > maxMCPText || !textMIME(v.MIME) {
			return nil, artifact.ErrInvalid
		}
		b, err := io.ReadAll(io.LimitReader(f, maxMCPText+1))
		if err != nil {
			return nil, err
		}
		if len(b) > maxMCPText || int64(len(b)) != v.Size || !utf8.Valid(b) {
			return nil, artifact.ErrInvalid
		}
		return map[string]any{"mode": in.Mode, "version": v, "text": string(b)}, nil
	}
	path := "/v2/artifacts/" + v.VersionID + "/content?store_id=" + v.StoreID + "&artifact_id=" + v.ArtifactID
	q := url.Values{}
	if a.ProjectID != "" {
		q.Set("project_id", a.ProjectID)
		q.Set("project_revision_id", a.RevisionID)
	}
	if a.AssetID != "" {
		q.Set("asset_id", a.AssetID)
		q.Set("asset_version_id", a.AssetVersionID)
	}
	if len(q) > 0 {
		path += "&" + q.Encode()
	}
	return map[string]any{"mode": in.Mode, "version": v, "content_path": path}, nil
}
