package main

import (
	"errors"
	"strings"

	"github.com/verdantflarehub/verdantflare-station-artifact/internal/artifact"
	"github.com/verdantflarehub/verdantflare-station-artifact/internal/authority"
)

// Legacy is an explicit transition mode preserving the deployed combined
// service. Split mode cannot silently reuse that service or its credentials.
func configuredAuthority(env func(string) string) (artifact.Authorizer, error) {
	base, err := configuredVideoAuthority(env)
	if err != nil {
		return nil, err
	}
	url, token, ca := env("ARTIFACT_SPEECH_AUTHORITY_URL"), env("ARTIFACT_SPEECH_AUTHORITY_TOKEN"), env("ARTIFACT_SPEECH_AUTHORITY_CA_FILE")
	if url == "" && token == "" && ca == "" {
		return base, nil
	}
	for _, key := range []string{"ARTIFACT_SERVICE_TOKEN", "ARTIFACT_AUTHORITY_TOKEN", "ARTIFACT_PROJECT_AUTHORITY_TOKEN", "ARTIFACT_WORLD_AUTHORITY_TOKEN", "ARTIFACT_VIDEO_AUTHORITY_TOKEN"} {
		if token == env(key) {
			return nil, errors.New("distinct Speech producer authority credential required")
		}
	}
	speech, err := authority.NewWithCA(url, token, strings.TrimSpace(ca))
	if err != nil {
		return nil, err
	}
	return authority.WithSpeech(base, speech)
}

func configuredVideoAuthority(env func(string) string) (artifact.Authorizer, error) {
	base, err := configuredBusinessAuthority(env)
	if err != nil {
		return nil, err
	}
	url, token, ca := env("ARTIFACT_VIDEO_AUTHORITY_URL"), env("ARTIFACT_VIDEO_AUTHORITY_TOKEN"), env("ARTIFACT_VIDEO_AUTHORITY_CA_FILE")
	if url == "" && token == "" && ca == "" {
		return base, nil
	}
	for _, key := range []string{"ARTIFACT_SERVICE_TOKEN", "ARTIFACT_AUTHORITY_TOKEN", "ARTIFACT_PROJECT_AUTHORITY_TOKEN", "ARTIFACT_WORLD_AUTHORITY_TOKEN"} {
		if token == env(key) {
			return nil, errors.New("distinct Video producer authority credential required")
		}
	}
	video, err := authority.NewWithCA(url, token, strings.TrimSpace(ca))
	if err != nil {
		return nil, err
	}
	return authority.WithVideo(base, video)
}

func configuredBusinessAuthority(env func(string) string) (artifact.Authorizer, error) {
	serviceToken := env("ARTIFACT_SERVICE_TOKEN")
	if len(serviceToken) < 32 || strings.ContainsAny(serviceToken, " \t\r\n") {
		return nil, errors.New("valid internal Artifact credential required")
	}
	mode := env("ARTIFACT_AUTHORITY_MODE")
	switch mode {
	case "", "legacy":
		token := env("ARTIFACT_AUTHORITY_TOKEN")
		if token == serviceToken {
			return nil, errors.New("distinct internal service/authority credentials are required")
		}
		return authority.NewWithCA(env("ARTIFACT_AUTHORITY_URL"), token, strings.TrimSpace(env("ARTIFACT_AUTHORITY_CA_FILE")))
	case "split":
		projectURL, worldURL := env("ARTIFACT_PROJECT_AUTHORITY_URL"), env("ARTIFACT_WORLD_AUTHORITY_URL")
		projectToken, worldToken := env("ARTIFACT_PROJECT_AUTHORITY_TOKEN"), env("ARTIFACT_WORLD_AUTHORITY_TOKEN")
		if projectURL == worldURL || projectToken == worldToken || projectToken == serviceToken || worldToken == serviceToken {
			return nil, errors.New("distinct Project and World authorities are required")
		}
		project, err := authority.NewWithCA(projectURL, projectToken, strings.TrimSpace(env("ARTIFACT_PROJECT_AUTHORITY_CA_FILE")))
		if err != nil {
			return nil, err
		}
		world, err := authority.NewWithCA(worldURL, worldToken, strings.TrimSpace(env("ARTIFACT_WORLD_AUTHORITY_CA_FILE")))
		if err != nil {
			return nil, err
		}
		return authority.NewRouter(project, world)
	default:
		return nil, errors.New("unsupported Artifact authority mode")
	}
}
