package main

import (
	"context"
	"testing"
)

func TestDisabledNodeAPIHasNoAppSecretOrNetworkDependency(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("NODE_API_ENABLED", "false")
	for _, name := range []string{"NODE_DATABASE_URL", "DATABASE_URL", "REDIS_URL", "SECRET_PEPPER_HEX", "JWT_ED25519_SEED_HEX", "FIELD_ENC_KEY_HEX", "NODE_TLS_SERVER_CERT", "NODE_TLS_SERVER_KEY", "NODE_TLS_CLIENT_CA"} {
		t.Setenv(name, "")
	}
	if err := run(context.Background()); err != nil {
		t.Fatal("disabled process tried to initialize dependencies", err)
	}
	t.Setenv("NODE_API_ENABLED", "true")
	if err := run(context.Background()); err == nil {
		t.Fatal("enabled process ignored incomplete configuration")
	}
}
