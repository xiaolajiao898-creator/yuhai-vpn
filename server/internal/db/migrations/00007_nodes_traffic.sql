-- +goose Up
-- =====================================================================
-- 6. 节点与流量
-- =====================================================================

CREATE TABLE node_groups (
  id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  name        VARCHAR(64)  NOT NULL UNIQUE,
  min_tier    SMALLINT     NOT NULL DEFAULT 1,
  remark      VARCHAR(255),
  created_at  TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE TABLE nodes (
  id                      BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  group_id                BIGINT       NOT NULL REFERENCES node_groups(id),
  name                    VARCHAR(64)  NOT NULL,
  country_code            CHAR(2)      NOT NULL,
  city                    VARCHAR(64)  NOT NULL,
  region                  VARCHAR(32)  NOT NULL,          -- ASIA / EUROPE / AMERICAS ...
  host                    VARCHAR(255) NOT NULL,
  probe_port              INT          NOT NULL DEFAULT 443,
  status                  TEXT         NOT NULL DEFAULT 'MAINTENANCE'
                          CHECK (status IN ('ONLINE', 'OFFLINE', 'MAINTENANCE', 'DISABLED')),
  weight                  INT          NOT NULL DEFAULT 100,
  capacity_users          INT          NOT NULL DEFAULT 500,
  is_recommended          BOOLEAN      NOT NULL DEFAULT false,
  load_score              SMALLINT     NOT NULL DEFAULT 0 CHECK (load_score BETWEEN 0 AND 100),
  online_count            INT          NOT NULL DEFAULT 0,
  agent_cert_fingerprint  CHAR(64),
  agent_version           VARCHAR(32),
  last_heartbeat_at       TIMESTAMPTZ,
  sort_order              INT          NOT NULL DEFAULT 0,
  created_at              TIMESTAMPTZ  NOT NULL DEFAULT now(),
  updated_at              TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE INDEX idx_nodes_group_status ON nodes (group_id, status);

CREATE TABLE node_inbounds (
  id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  node_id        BIGINT       NOT NULL REFERENCES nodes(id),
  protocol       TEXT         NOT NULL CHECK (protocol IN (
                   'VLESS_REALITY', 'TROJAN', 'SS2022', 'WIREGUARD', 'HYSTERIA2')),
  port           INT          NOT NULL CHECK (port BETWEEN 1 AND 65535),
  public_params  JSONB        NOT NULL,     -- 可下发客户端的参数：SNI、Reality 公钥、short_id、WG 服务端公钥等
  priority       INT          NOT NULL DEFAULT 100,
  status         TEXT         NOT NULL DEFAULT 'ENABLED' CHECK (status IN ('ENABLED', 'DISABLED')),
  created_at     TIMESTAMPTZ  NOT NULL DEFAULT now(),
  UNIQUE (node_id, protocol, port)
);

CREATE TABLE node_enroll_tokens (
  id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  node_id     BIGINT       NOT NULL REFERENCES nodes(id),
  token_hash  CHAR(64)     NOT NULL UNIQUE,
  expires_at  TIMESTAMPTZ  NOT NULL,
  used_at     TIMESTAMPTZ,
  created_by  BIGINT       NOT NULL,
  created_at  TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE TABLE node_user_events (
  id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,   -- agent 增量游标
  credential_id   BIGINT       NOT NULL REFERENCES device_credentials(id),
  effective_tier  SMALLINT     NOT NULL,      -- 0 表示不可连接；节点按 tier >= 组 min_tier 决定增删
  reason          VARCHAR(32)  NOT NULL,      -- PURCHASE / EXPIRE / QUOTA / REVOKE / BAN / ROTATE
  created_at      TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE TABLE traffic_reports (
  node_id       BIGINT       NOT NULL REFERENCES nodes(id),
  report_id     UUID         NOT NULL,
  period_start  TIMESTAMPTZ  NOT NULL,
  period_end    TIMESTAMPTZ  NOT NULL,
  item_count    INT          NOT NULL,
  total_up      BIGINT       NOT NULL,
  total_down    BIGINT       NOT NULL,
  received_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
  PRIMARY KEY (node_id, report_id)
);

CREATE TABLE traffic_daily (
  stat_date       DATE    NOT NULL,
  device_id       BIGINT  NOT NULL,
  node_id         BIGINT  NOT NULL,
  user_id         BIGINT  NOT NULL,
  upload_bytes    BIGINT  NOT NULL DEFAULT 0,
  download_bytes  BIGINT  NOT NULL DEFAULT 0,
  PRIMARY KEY (stat_date, device_id, node_id)
) PARTITION BY RANGE (stat_date);
CREATE INDEX idx_traffic_daily_user ON traffic_daily (user_id, stat_date);
-- 分区示例：CREATE TABLE traffic_daily_2026_09 PARTITION OF traffic_daily
--           FOR VALUES FROM ('2026-09-01') TO ('2026-10-01');

-- +goose Down
-- +goose StatementBegin
-- 不提供自动回滚：账务表回滚需人工评审后执行
SELECT 1;
-- +goose StatementEnd
