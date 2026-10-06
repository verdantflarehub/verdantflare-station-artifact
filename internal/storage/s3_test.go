package storage

import "testing"

var _ Backend = (*Local)(nil)
var _ Backend = (*S3)(nil)

func TestNewS3RequiresExplicitCredentialsAndEndpoint(t *testing.T) {
	for name, endpoint := range map[string]string{
		"missing endpoint":     "",
		"endpoint with scheme": "https://s3.example.test",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewS3(endpoint, "artifact", "access", "secret", "", true, 1<<20); err != ErrInvalid {
				t.Fatalf("NewS3(%q) error = %v, want ErrInvalid", endpoint, err)
			}
		})
	}
	if _, err := NewS3("s3.example.test", "artifact", "", "secret", "", true, 1<<20); err != ErrInvalid {
		t.Fatalf("missing access key error = %v, want ErrInvalid", err)
	}
	if _, err := NewS3("s3.example.test", "artifact", "access", "secret", "", true, 1<<20); err != nil {
		t.Fatalf("valid S3 configuration rejected: %v", err)
	}
}
