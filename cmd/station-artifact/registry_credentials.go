package main

import (
	"errors"
	"net/url"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

// registryClientConfig supports the existing anonymous deployment until its
// controlled migration. Configured identities are preserved in the native client configuration.
func registryClientConfig(endpoints []string, lookup func(string) string) (clientv3.Config, error) {
	user, password := lookup("ETCD_USERNAME"), lookup("ETCD_PASSWORD")
	if len(endpoints) == 0 || (user == "") != (password == "") {
		return clientv3.Config{}, errors.New("invalid registry configuration")
	}
	for _, endpoint := range endpoints {
		address := endpoint
		if !strings.Contains(address, "://") {
			address = "http://" + address
		}
		parsed, err := url.Parse(address)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil ||
			(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || strings.ContainsAny(endpoint, " \t\r\n") {
			return clientv3.Config{}, errors.New("invalid registry configuration")
		}
	}
	return clientv3.Config{Endpoints: append([]string(nil), endpoints...), Username: user, Password: password,
		DialTimeout: 5 * time.Second, Logger: zap.NewNop(), DialOptions: []grpc.DialOption{grpc.WithNoProxy()}}, nil
}
