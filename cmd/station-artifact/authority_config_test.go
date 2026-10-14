package main

import (
	"strings"
	"testing"

	"github.com/verdantflarehub/verdantflare-station-artifact/internal/authority"
)

func authorityEnvironment() map[string]string {
	return map[string]string{
		"ARTIFACT_SERVICE_TOKEN": strings.Repeat("s", 32), "ARTIFACT_AUTHORITY_URL": "https://legacy.example/internal/v1/artifact/authorize", "ARTIFACT_AUTHORITY_TOKEN": strings.Repeat("l", 32),
		"ARTIFACT_PROJECT_AUTHORITY_URL": "https://project.example/internal/v1/artifact/authorize", "ARTIFACT_PROJECT_AUTHORITY_TOKEN": strings.Repeat("p", 32),
		"ARTIFACT_WORLD_AUTHORITY_URL": "https://world.example/internal/v1/artifact/authorize", "ARTIFACT_WORLD_AUTHORITY_TOKEN": strings.Repeat("w", 32),
	}
}

func TestAuthorityConfigurationModes(t *testing.T) {
	for _, mode := range []string{"", "legacy", "split"} {
		t.Run(mode, func(t *testing.T) {
			env := authorityEnvironment()
			env["ARTIFACT_AUTHORITY_MODE"] = mode
			got, err := configuredAuthority(func(k string) string { return env[k] })
			if err != nil {
				t.Fatal(err)
			}
			_, split := got.(*authority.Router)
			if split != (mode == "split") {
				t.Fatal("incorrect authority mode")
			}
		})
	}
}

func TestAuthorityConfigurationSplitFailsClosed(t *testing.T) {
	for _, kind := range []string{"missing_world", "shared_tokens", "service_token", "shared_endpoint", "unknown_mode", "insecure_world"} {
		t.Run(kind, func(t *testing.T) {
			env := authorityEnvironment()
			env["ARTIFACT_AUTHORITY_MODE"] = "split"
			switch kind {
			case "missing_world":
				delete(env, "ARTIFACT_WORLD_AUTHORITY_TOKEN")
			case "shared_tokens":
				env["ARTIFACT_WORLD_AUTHORITY_TOKEN"] = env["ARTIFACT_PROJECT_AUTHORITY_TOKEN"]
			case "service_token":
				env["ARTIFACT_WORLD_AUTHORITY_TOKEN"] = env["ARTIFACT_SERVICE_TOKEN"]
			case "shared_endpoint":
				env["ARTIFACT_WORLD_AUTHORITY_URL"] = env["ARTIFACT_PROJECT_AUTHORITY_URL"]
			case "unknown_mode":
				env["ARTIFACT_AUTHORITY_MODE"] = "fallback"
			case "insecure_world":
				env["ARTIFACT_WORLD_AUTHORITY_URL"] = "http://world.example/check"
			}
			if _, err := configuredAuthority(func(k string) string { return env[k] }); err == nil {
				t.Fatal("invalid split mode fell back to legacy")
			}
		})
	}
}

func TestVideoProducerConfigurationIsOptionalAndIsolated(t *testing.T) {
	for _, kind := range []string{"valid", "missing_token", "shared_token", "insecure_url"} {
		t.Run(kind, func(t *testing.T) {
			env := authorityEnvironment()
			env["ARTIFACT_AUTHORITY_MODE"] = "split"
			env["ARTIFACT_VIDEO_AUTHORITY_URL"] = "https://video.example/internal/artifacts/authorize"
			env["ARTIFACT_VIDEO_AUTHORITY_TOKEN"] = strings.Repeat("v", 32)
			switch kind {
			case "missing_token":
				delete(env, "ARTIFACT_VIDEO_AUTHORITY_TOKEN")
			case "shared_token":
				env["ARTIFACT_VIDEO_AUTHORITY_TOKEN"] = env["ARTIFACT_SERVICE_TOKEN"]
			case "insecure_url":
				env["ARTIFACT_VIDEO_AUTHORITY_URL"] = "http://video.example/authorize"
			}
			got, err := configuredAuthority(func(k string) string { return env[k] })
			if kind == "valid" {
				if _, ok := got.(*authority.VideoNative); !ok || err != nil {
					t.Fatal("Video producer was not configured")
				}
			} else if err == nil {
				t.Fatal("invalid producer configuration accepted")
			}
		})
	}
}
