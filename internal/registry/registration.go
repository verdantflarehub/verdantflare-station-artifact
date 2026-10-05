package registry

import (
	"net/url"
	"regexp"
	"strings"
	"time"
)

const ServicesPrefix = "/verdantflare/mcp/services/"
const DefaultTimeout = 30 * time.Second

type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}
type ServiceRegistration struct {
	Domain         string           `json:"domain"`
	Endpoint       string           `json:"endpoint"`
	HealthEndpoint string           `json:"health_endpoint"`
	Version        string           `json:"version"`
	UpdatedAt      string           `json:"updated_at"`
	Tools          []ToolDefinition `json:"tools"`
}

var domainPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
var toolPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$`)
var versionPattern = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+.*$`)

func endpointValid(raw string) bool {
	u, e := url.Parse(raw)
	return e == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https") && u.User == nil && u.RawQuery == "" && u.Fragment == ""
}
func validRegistration(key string, r ServiceRegistration) bool {
	if key != ServicesPrefix+r.Domain || !domainPattern.MatchString(r.Domain) || !endpointValid(r.Endpoint) || !endpointValid(r.HealthEndpoint) || !versionPattern.MatchString(r.Version) || r.Tools == nil {
		return false
	}
	if _, e := time.Parse(time.RFC3339, r.UpdatedAt); e != nil {
		return false
	}
	seen := map[string]bool{}
	for _, t := range r.Tools {
		if !toolPattern.MatchString(t.Name) || !strings.HasPrefix(t.Name, r.Domain+".") || t.Description == "" || t.InputSchema == nil || seen[t.Name] {
			return false
		}
		seen[t.Name] = true
	}
	return true
}
