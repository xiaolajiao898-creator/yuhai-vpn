package config

import (
	"os"
	"strings"
	"testing"
)

func testConfigEnv(t *testing.T) {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATABASE_URL", "postgres://fixture.invalid/test")
	t.Setenv("DEV_TOOLS_ENABLED", "false")
	t.Setenv("SECRET_PEPPER_HEX", strings.Repeat("01", 32))
	t.Setenv("JWT_ED25519_SEED_HEX", strings.Repeat("02", 32))
	t.Setenv("FIELD_ENC_KEY_HEX", strings.Repeat("03", 32))
}

func TestInviteRewardsDefaultsDisabledAndExplicitValues(t *testing.T) {
	testConfigEnv(t)
	// Setenv records the inherited value for cleanup; remove it to test the
	// real unset default independently of the machine running this test.
	t.Setenv("INVITE_REWARDS_ENABLED", "false")
	if err := os.Unsetenv("INVITE_REWARDS_ENABLED"); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil || cfg.InviteRewardsEnabled {
		t.Fatal("invitation rewards must default off", cfg.InviteRewardsEnabled, err)
	}
	for _, value := range []string{"false", "true"} {
		t.Setenv("INVITE_REWARDS_ENABLED", value)
		cfg, err := Load()
		if err != nil || cfg.InviteRewardsEnabled != (value == "true") {
			t.Fatal("explicit reward gate not respected", value, cfg.InviteRewardsEnabled, err)
		}
	}
}

func TestInviteRewardsInvalidValuesRejectConfiguration(t *testing.T) {
	testConfigEnv(t)
	for _, value := range []string{"", "TRUE", "False", " true", "false ", "1", "0", "yes", "enabled", "tru"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("INVITE_REWARDS_ENABLED", value)
			cfg, err := Load()
			if err == nil || !strings.Contains(err.Error(), "INVITE_REWARDS_ENABLED") || cfg.InviteRewardsEnabled {
				t.Fatal("invalid reward gate accepted", value, cfg.InviteRewardsEnabled, err)
			}
		})
	}
}

func TestInviteRewardsCannotBypassProductionDevelopmentGuard(t *testing.T) {
	testConfigEnv(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("DEV_TOOLS_ENABLED", "true")
	t.Setenv("INVITE_REWARDS_ENABLED", "true")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "DEV_TOOLS_ENABLED") {
		t.Fatal("existing production guard bypassed", err)
	}
}

func TestWGAddressPoolDefaultsAndRejectsUnsafeValues(t *testing.T) {
	testConfigEnv(t)
	t.Setenv("WG_ADDRESS_POOL", "10.20.0.0/16")
	if err := os.Unsetenv("WG_ADDRESS_POOL"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WG_ADDRESS_RESERVED", "16")
	if err := os.Unsetenv("WG_ADDRESS_RESERVED"); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WGAddressPool == nil || cfg.WGAddressPool.String() != "10.20.0.0/16" || cfg.WGAddressReserved != 16 {
		t.Fatalf("默认地址池不符：%v %d", cfg.WGAddressPool, cfg.WGAddressReserved)
	}

	// 把公网网段配成隧道地址池，会让用户流量走错方向——必须在启动期就拒绝，
	// 而不是等到某台设备真的拿到这个地址才暴露。
	for _, bad := range []string{"8.8.8.0/24", "203.0.113.0/24", "10.0.0.0/8", "10.1.2.0/29",
		"fd00::/64", "10.20.0.0", "not-a-cidr"} {
		t.Setenv("WG_ADDRESS_POOL", bad)
		if _, err := Load(); err == nil {
			t.Fatalf("地址池 %q 应被拒绝", bad)
		}
	}

	t.Setenv("WG_ADDRESS_POOL", "192.168.0.0/24")
	for _, bad := range []string{"0", "-1", "255", "abc"} {
		t.Setenv("WG_ADDRESS_RESERVED", bad)
		if _, err := Load(); err == nil {
			t.Fatalf("保留数 %q 应被拒绝", bad)
		}
	}
	t.Setenv("WG_ADDRESS_RESERVED", "10")
	cfg, err = Load()
	if err != nil || cfg.WGAddressReserved != 10 || cfg.WGAddressPool.String() != "192.168.0.0/24" {
		t.Fatalf("显式配置未生效：%v %d %v", cfg.WGAddressPool, cfg.WGAddressReserved, err)
	}
}

func TestDevelopmentFlagRejectsAmbiguousValues(t *testing.T) {
	testConfigEnv(t)
	for _, value := range []string{"TRUE", "False", "1", "yes", " true", "false ", ""} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("DEV_TOOLS_ENABLED", value)
			if _, err := Load(); err == nil {
				t.Fatal("ambiguous development flag accepted")
			}
		})
	}
}

func TestProductionRequiresIndependentKeysAndFormalIdentifiers(t *testing.T) {
	testConfigEnv(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("SECRET_PEPPER_HEX", "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f")
	t.Setenv("JWT_ED25519_SEED_HEX", "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20")
	t.Setenv("FIELD_ENC_KEY_HEX", "02030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f2021")
	t.Setenv("FUND_ADDRESS_PUBKEY_B64", "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=")
	t.Setenv("JWT_KEY_ID", "jwt-prod-2026")
	t.Setenv("FIELD_ENC_KEY_ID", "fek-prod-2026")
	t.Setenv("FUND_ADDRESS_KEY_ID", "fund-prod-2026")
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ key, value string }{
		{"JWT_KEY_ID", "jwt-dev-1"}, {"FIELD_ENC_KEY_ID", ""}, {"FUND_ADDRESS_KEY_ID", "fund-dev-1"},
		{"JWT_ED25519_SEED_HEX", os.Getenv("SECRET_PEPPER_HEX")},
		{"FIELD_ENC_KEY_HEX", os.Getenv("JWT_ED25519_SEED_HEX")},
		{"FUND_ADDRESS_PUBKEY_B64", "not-a-public-key"},
	} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)
			if _, err := Load(); err == nil {
				t.Fatal("unsafe production configuration accepted")
			}
		})
	}
}
