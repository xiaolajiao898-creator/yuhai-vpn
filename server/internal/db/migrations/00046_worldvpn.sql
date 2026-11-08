-- +goose Up
-- WorldVPN 批发账号与宇海用户的对应关系。加在已有的 42–45（兑换码、试用、网页试用、USDT）之后。
-- 不创建、不修改 redeem_codes。兑换码仍用 duration_days、批次和 unused/used/revoked。
-- 批发登录密码只来自环境变量。这里只存每个用户在 WorldVPN 上的 VPN 口令（字段加密）。
-- 不保存线路 IP：线路列表每次向 listServers 现取。

CREATE TABLE worldvpn_state (
  singleton   BOOLEAN      PRIMARY KEY DEFAULT TRUE CHECK (singleton),
  sync_after  TIMESTAMPTZ  NOT NULL
);

CREATE TABLE worldvpn_accounts (
  user_id       BIGINT       PRIMARY KEY REFERENCES users(id),
  vpn_username  TEXT         NOT NULL UNIQUE CHECK (char_length(vpn_username) BETWEEN 1 AND 64),
  password_enc  BYTEA        NOT NULL,
  status        TEXT         NOT NULL CHECK (status IN ('ACTIVE','DISABLED')),
  expire_at     TIMESTAMPTZ  NOT NULL,
  source        TEXT         NOT NULL CHECK (source IN ('ORDER','REDEEM','TRIAL')),
  sandbox       BOOLEAN      NOT NULL,
  updated_at    TIMESTAMPTZ  NOT NULL
);

CREATE TABLE worldvpn_grants (
  id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  source         TEXT        NOT NULL CHECK (source IN ('ORDER','REDEEM','TRIAL')),
  source_id      BIGINT      NOT NULL,
  user_id        BIGINT      NOT NULL REFERENCES users(id),
  period_months  INT         NOT NULL CHECK (period_months IN (0,1,3,12)),
  status         TEXT        NOT NULL CHECK (status IN ('PENDING','DONE','SKIPPED')),
  last_error     TEXT        NOT NULL DEFAULT '' CHECK (char_length(last_error) <= 200),
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (source, source_id)
);
CREATE INDEX worldvpn_grants_pending ON worldvpn_grants (id) WHERE status = 'PENDING';

CREATE TABLE worldvpn_peers (
  id                    BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id               BIGINT      NOT NULL REFERENCES users(id),
  device_id             BIGINT      NOT NULL REFERENCES devices(id),
  server_id             BIGINT      NOT NULL CHECK (server_id > 0),
  wireguard_id          TEXT        NOT NULL CHECK (char_length(wireguard_id) BETWEEN 1 AND 32),
  sandbox               BOOLEAN     NOT NULL,
  release_requested_at  TIMESTAMPTZ,
  released_at           TIMESTAMPTZ,
  created_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX worldvpn_peers_one_open_device
  ON worldvpn_peers (device_id) WHERE released_at IS NULL;

GRANT SELECT, INSERT, UPDATE ON
  worldvpn_state, worldvpn_accounts, worldvpn_grants, worldvpn_peers
  TO yuhai_app;
GRANT USAGE, SELECT ON SEQUENCE
  worldvpn_grants_id_seq, worldvpn_peers_id_seq
  TO yuhai_app;
REVOKE DELETE, TRUNCATE ON
  worldvpn_state, worldvpn_accounts, worldvpn_grants, worldvpn_peers
  FROM yuhai_app;

-- +goose Down
DROP TABLE worldvpn_peers;
DROP TABLE worldvpn_grants;
DROP TABLE worldvpn_accounts;
DROP TABLE worldvpn_state;
