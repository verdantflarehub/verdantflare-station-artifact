package main

import (
	"strings"
	"testing"
)

func TestRegistryCredentialsConfiguration(t *testing.T) {
	for _, tc := range []struct {
		user, password string
		rejected       bool
	}{
		{"", "", false}, {"writer", "synthetic-password", false}, {"writer", "", true}, {"", "synthetic-password", true},
	} {
		cfg, err := registryClientConfig([]string{"http://etcd.test:2379"}, func(key string) string {
			if key == "ETCD_USERNAME" {
				return tc.user
			}
			return tc.password
		})
		if (err != nil) != tc.rejected {
			t.Fatal("unexpected credential validation result")
		}
		if err != nil {
			if strings.Contains(err.Error(), "synthetic-password") {
				t.Fatal("credential leaked in error")
			}
		} else if cfg.Username != tc.user || cfg.Password != tc.password {
			t.Fatal("configured identity was dropped")
		}
	}
}

func TestRegistryRejectsEmbeddedCredentialsAndRedirectPaths(t *testing.T) {
	for _, endpoint := range []string{"http://user:synthetic-password@host:2379", "http://host:2379/collect", "http://host:2379?secret=value", "file:///tmp/registry", "http://host:2379#fragment"} {
		_, err := registryClientConfig([]string{endpoint}, func(string) string { return "" })
		if err == nil || strings.Contains(err.Error(), "synthetic-password") {
			t.Fatal("unsafe endpoint accepted or leaked")
		}
	}
	if _, err := registryClientConfig(nil, func(string) string { return "" }); err == nil {
		t.Fatal("empty endpoints accepted")
	}
	if _, err := registryClientConfig([]string{"127.0.0.1:2379"}, func(string) string { return "" }); err != nil {
		t.Fatal("legacy endpoint rejected")
	}
}
