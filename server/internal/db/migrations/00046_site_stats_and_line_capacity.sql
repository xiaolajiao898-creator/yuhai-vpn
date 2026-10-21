-- +goose Up
-- 官网访问与下载统计，以及出口人数上限。
--
-- 访问和下载只存 IP 的 HMAC（32 字节），不存明文地址。计数按 Asia/Kuala_Lumpur 的自然日切。
-- 出口没有心跳，capacity_users = 0 表示不设上限，不能当成「已满」。

INSERT INTO admin_permissions(code,name,risk_level) VALUES
 ('site.stats.read','查看官网访问与下载统计',0)
 ON CONFLICT(code) DO NOTHING;

INSERT INTO admin_role_permissions(role_id,permission_code)
 SELECT r.id, 'site.stats.read' FROM admin_roles r WHERE r.code='super_admin'
 ON CONFLICT DO NOTHING;

CREATE TABLE site_daily_metrics (
  day    DATE   NOT NULL,
  metric TEXT   NOT NULL CHECK (metric IN ('pageview','download_android','download_windows','download_android_legacy')),
  count  BIGINT NOT NULL DEFAULT 0 CHECK (count >= 0),
  PRIMARY KEY (day, metric)
);

CREATE TABLE site_visitor_days (
  day     DATE  NOT NULL,
  ip_hash BYTEA NOT NULL CHECK (octet_length(ip_hash) = 32),
  PRIMARY KEY (day, ip_hash)
);

CREATE TABLE site_visitors (
  ip_hash   BYTEA PRIMARY KEY CHECK (octet_length(ip_hash) = 32),
  first_day DATE NOT NULL
);

CREATE TABLE site_download_dedupe (
  day     DATE  NOT NULL,
  channel TEXT  NOT NULL CHECK (channel IN ('android','windows','android-legacy')),
  ip_hash BYTEA NOT NULL CHECK (octet_length(ip_hash) = 32),
  PRIMARY KEY (day, channel, ip_hash)
);

ALTER TABLE egress_endpoints
  ADD COLUMN capacity_users INT NOT NULL DEFAULT 0
  CHECK (capacity_users BETWEEN 0 AND 1000000);

REVOKE ALL ON site_daily_metrics, site_visitor_days, site_visitors, site_download_dedupe FROM PUBLIC;
GRANT SELECT, INSERT, UPDATE ON site_daily_metrics, site_visitor_days, site_visitors, site_download_dedupe TO yuhai_app, yuhai_admin;
GRANT SELECT ON site_daily_metrics, site_visitor_days, site_visitors, site_download_dedupe TO yuhai_readonly;
REVOKE DELETE, TRUNCATE ON site_daily_metrics, site_visitor_days, site_visitors, site_download_dedupe FROM yuhai_app, yuhai_admin, PUBLIC;

-- +goose Down
ALTER TABLE egress_endpoints DROP COLUMN capacity_users;
DROP TABLE site_download_dedupe;
DROP TABLE site_visitors;
DROP TABLE site_visitor_days;
DROP TABLE site_daily_metrics;
DELETE FROM admin_role_permissions WHERE permission_code = 'site.stats.read';
DELETE FROM admin_permissions WHERE code = 'site.stats.read';
