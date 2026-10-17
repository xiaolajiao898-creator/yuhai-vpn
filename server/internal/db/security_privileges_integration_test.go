package db

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSecurityPrivilegeMigrationIntegration(t *testing.T) {
	ownerURL, appURL := os.Getenv("TEST_MIGRATE_DATABASE_URL"), os.Getenv("TEST_DATABASE_URL")
	if ownerURL == "" || appURL == "" {
		t.Skip("requires isolated database on port 15432")
	}
	ownerCfg, err := pgxpool.ParseConfig(ownerURL)
	if err != nil {
		t.Fatal("invalid owner config")
	}
	appCfg, err := pgxpool.ParseConfig(appURL)
	if err != nil {
		t.Fatal("invalid app config")
	}
	if ownerCfg.ConnConfig.Port != 15432 || appCfg.ConnConfig.Port != 15432 || ownerCfg.ConnConfig.Host != appCfg.ConnConfig.Host || ownerCfg.ConnConfig.Database != appCfg.ConnConfig.Database || appCfg.ConnConfig.User != "yuhai_app" {
		t.Fatal("requires isolated matching database and actual app role")
	}
	ctx := context.Background()
	owner, err := pgxpool.NewWithConfig(ctx, ownerCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	schema := "security_privilege_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	q := pgx.Identifier{schema}.Sanitize()
	if _, err = owner.Exec(ctx, "CREATE SCHEMA "+q); err != nil {
		t.Fatal(err)
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := owner.Exec(c, "DROP SCHEMA "+q+" CASCADE"); err != nil {
			t.Error(err)
		}
	}()
	authTables := []string{"admin_users", "admin_roles", "admin_permissions", "admin_role_permissions", "admin_user_roles", "admin_sessions", "admin_step_up_tokens", "admin_mfa_tickets", "admin_login_limits", "admin_approvals", "admin_management_requests", "admin_version_mutations", "admin_device_mutations", "admin_node_mutations", "admin_node_security_mutations", "admin_risk_mutations", "admin_invite_mutations", "admin_plan_mutations", "admin_config_mutations", "admin_finance_mutations", "admin_export_mutations"}
	ledgers := []string{"balance_ledger", "subscription_ledger", "device_bind_logs", "admin_audit_logs"}
	tables := append(append(append([]string{}, authTables...), ledgers...), "orders", "chain_transfers", "admin_exports")
	for _, table := range tables {
		if _, err = owner.Exec(ctx, "CREATE TABLE "+pgx.Identifier{schema, table}.Sanitize()+" (id integer)"); err != nil {
			t.Fatal(err)
		}
	}
	// Model the unsafe original broad grants, then execute the real migration.
	if _, err = owner.Exec(ctx, "GRANT USAGE ON SCHEMA "+q+" TO yuhai_app,yuhai_admin,yuhai_readonly; GRANT ALL ON ALL TABLES IN SCHEMA "+q+" TO yuhai_app,yuhai_admin,yuhai_readonly"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("migrations/00039_security_privileges.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.Split(string(raw), "-- +goose Down")[0]
	tx, err := owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SET LOCAL search_path TO "+q); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, sql); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	appCfg.ConnConfig.RuntimeParams["search_path"] = schema
	app, err := pgxpool.NewWithConfig(ctx, appCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	deny := func(query string) {
		t.Helper()
		_, err := app.Exec(ctx, query)
		var pgerr *pgconn.PgError
		if !errors.As(err, &pgerr) || pgerr.Code != "42501" {
			t.Fatalf("query %s must fail by privilege, got %v", query, err)
		}
	}
	for _, table := range authTables {
		deny("SELECT * FROM " + table)
		deny("INSERT INTO " + table + "(id) VALUES (1)")
		deny("UPDATE " + table + " SET id=2")
		deny("DELETE FROM " + table)
	}
	for _, table := range ledgers {
		if _, err = app.Exec(ctx, "INSERT INTO "+table+"(id) VALUES(1)"); err != nil {
			t.Fatal("append denied", table, err)
		}
		for _, verb := range []string{"UPDATE", "DELETE", "TRUNCATE"} {
			for _, role := range []string{"yuhai_app", "yuhai_admin"} {
				var granted bool
				if err = owner.QueryRow(ctx, "SELECT has_table_privilege($1,$2,$3)", role, schema+"."+table, verb).Scan(&granted); err != nil || granted {
					t.Fatalf("%s %s %s granted=%v err=%v", role, verb, table, granted, err)
				}
			}
		}
		deny("UPDATE " + table + " SET id=2")
		deny("DELETE FROM " + table)
		deny("TRUNCATE " + table)
	}
	for _, table := range []string{"admin_users", "admin_sessions", "admin_step_up_tokens", "admin_mfa_tickets", "admin_management_requests", "admin_approvals"} {
		var granted bool
		if err = owner.QueryRow(ctx, "SELECT has_table_privilege('yuhai_readonly',$1,'SELECT')", schema+"."+table).Scan(&granted); err != nil || granted {
			t.Fatalf("sensitive table exposed to reporting role: %s", table)
		}
	}
	if _, err = app.Exec(ctx, "INSERT INTO admin_exports(id) VALUES(1); UPDATE admin_exports SET id=2; DELETE FROM admin_exports"); err != nil {
		t.Fatal("worker export grant lost", err)
	}
}
