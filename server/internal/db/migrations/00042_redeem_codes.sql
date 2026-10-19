-- +goose Up
-- 兑换码（周卡 / 月卡 / 年卡）。
--
-- 套餐表 prices 尚未确定，所以卡密不引用 plans。期限写在 redeem_card_types，
-- 生成时把当时的天数、等级、设备数、流量快照到每一张码上。以后改卡的期限只影响新生成的码：
--   UPDATE redeem_card_types SET duration_days = 31 WHERE code = 'MONTH';
-- 已发出的码仍按生成时的快照兑换。
--
-- 明文只在生成和导出时经字段加密解出。表里存的是 SHA-256(去掉连字符的大写码)，
-- 数据库泄漏不能直接拿去兑换。codes_enc 用字段密钥加密，没有密钥解不出明文。
--
-- 叠加规则在 entitlement.ApplyRedeem：已有未过期权益时，从当前到期时间往后加天数，
-- 不把更高的等级、更多的设备或已有的不限流量改小。没有有效权益时按卡本身开通。

INSERT INTO admin_permissions(code,name,risk_level) VALUES
 ('redeem_codes.read','查看兑换码（不含明文）',0),
 ('redeem_codes.generate','生成兑换码',2),
 ('redeem_codes.export','导出已生成兑换码的明文',2),
 ('redeem_codes.revoke','作废未使用的兑换码',2)
 ON CONFLICT(code) DO NOTHING;

INSERT INTO admin_role_permissions(role_id,permission_code)
 SELECT r.id, p.code
   FROM admin_roles r
   CROSS JOIN (VALUES
     ('redeem_codes.read'),
     ('redeem_codes.generate'),
     ('redeem_codes.export'),
     ('redeem_codes.revoke')) AS p(code)
  WHERE r.code = 'super_admin'
 ON CONFLICT DO NOTHING;

CREATE TABLE redeem_card_types (
  code           TEXT         PRIMARY KEY CHECK (code ~ '^[A-Z][A-Z0-9_]{0,15}$'),
  name           TEXT         NOT NULL CHECK (char_length(name) BETWEEN 1 AND 32),
  duration_days  INT          NOT NULL CHECK (duration_days > 0 AND duration_days <= 3650),
  tier           SMALLINT     NOT NULL CHECK (tier BETWEEN 1 AND 9),
  device_limit   INT          NOT NULL CHECK (device_limit > 0 AND device_limit <= 100),
  traffic_bytes  BIGINT       CHECK (traffic_bytes IS NULL OR traffic_bytes > 0),
  enabled        BOOLEAN      NOT NULL DEFAULT true,
  created_at     TIMESTAMPTZ  NOT NULL DEFAULT now()
);

-- 老板定的三档。流量 NULL = 不限。等级 L1，3 台设备。
INSERT INTO redeem_card_types(code,name,duration_days,tier,device_limit,traffic_bytes) VALUES
 ('WEEK','周卡',7,1,3,NULL),
 ('MONTH','月卡',30,1,3,NULL),
 ('YEAR','年卡',365,1,3,NULL);

CREATE TABLE redeem_batches (
  id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  card_type    TEXT         NOT NULL REFERENCES redeem_card_types(code),
  quantity     INT          NOT NULL CHECK (quantity BETWEEN 1 AND 500),
  note         VARCHAR(200),
  codes_enc    BYTEA        NOT NULL,
  created_by   BIGINT       NOT NULL REFERENCES admin_users(id),
  created_at   TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE TABLE redeem_codes (
  id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  code_hash      CHAR(64)     NOT NULL UNIQUE CHECK (code_hash ~ '^[a-f0-9]{64}$'),
  code_hint      CHAR(4)      NOT NULL CHECK (code_hint ~ '^[ABCDEFGHJKLMNPQRSTUVWXYZ23456789]{4}$'),
  batch_id       BIGINT       NOT NULL REFERENCES redeem_batches(id),
  card_type      TEXT         NOT NULL REFERENCES redeem_card_types(code),
  duration_days  INT          NOT NULL CHECK (duration_days > 0 AND duration_days <= 3650),
  tier           SMALLINT     NOT NULL CHECK (tier BETWEEN 1 AND 9),
  device_limit   INT          NOT NULL CHECK (device_limit > 0 AND device_limit <= 100),
  traffic_bytes  BIGINT       CHECK (traffic_bytes IS NULL OR traffic_bytes > 0),
  note           VARCHAR(200),
  created_by     BIGINT       NOT NULL REFERENCES admin_users(id),
  created_at     TIMESTAMPTZ  NOT NULL DEFAULT now(),
  status         TEXT         NOT NULL DEFAULT 'unused' CHECK (status IN ('unused','used','revoked')),
  redeemed_by    BIGINT       REFERENCES users(id),
  redeemed_at    TIMESTAMPTZ,
  revoked_by     BIGINT       REFERENCES admin_users(id),
  revoked_at     TIMESTAMPTZ,
  CHECK (
       (status = 'unused' AND redeemed_by IS NULL AND redeemed_at IS NULL AND revoked_by IS NULL AND revoked_at IS NULL)
    OR (status = 'used' AND redeemed_by IS NOT NULL AND redeemed_at IS NOT NULL AND revoked_by IS NULL AND revoked_at IS NULL)
    OR (status = 'revoked' AND redeemed_by IS NULL AND redeemed_at IS NULL AND revoked_by IS NOT NULL AND revoked_at IS NOT NULL)
  )
);

CREATE INDEX redeem_codes_list_idx ON redeem_codes (status, card_type, id DESC);
CREATE INDEX redeem_codes_batch_idx ON redeem_codes (batch_id, id);
CREATE INDEX redeem_codes_note_idx ON redeem_codes (note) WHERE note IS NOT NULL;

CREATE TABLE admin_redeem_mutations (
  admin_user_id BIGINT      NOT NULL REFERENCES admin_users(id),
  operation_id  UUID        NOT NULL,
  action_code   TEXT        NOT NULL CHECK (action_code IN ('redeem_codes.generate','redeem_codes.export','redeem_codes.revoke')),
  request_hash  CHAR(64)    NOT NULL CHECK (request_hash ~ '^[a-f0-9]{64}$'),
  target_id     BIGINT      NOT NULL CHECK (target_id > 0),
  session_id    BIGINT      NOT NULL,
  proof_hash    CHAR(64)    NOT NULL CHECK (proof_hash ~ '^[a-f0-9]{64}$'),
  response      JSON        NOT NULL,
  created_at    TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (operation_id)
);

CREATE INDEX admin_redeem_mutations_admin_idx ON admin_redeem_mutations (admin_user_id, created_at);

-- 兑换流水要能写明来源。原约束在 00003，名字是 PostgreSQL 给列检查起的默认名。
ALTER TABLE subscription_ledger DROP CONSTRAINT IF EXISTS subscription_ledger_change_type_check;
ALTER TABLE subscription_ledger ADD CONSTRAINT subscription_ledger_change_type_check
  CHECK (change_type IN (
    'PURCHASE', 'RENEW', 'TIER_CHANGE', 'TRAFFIC_PACK', 'DEVICE_ADDON',
    'INVITE_REWARD', 'ADMIN_ADJUST', 'TRAFFIC_CONSUME_OVERFLOW',
    'PERIOD_RESET', 'EXPIRE', 'REVOKE', 'REDEEM'));

ALTER TABLE subscription_ledger DROP CONSTRAINT IF EXISTS subscription_ledger_source_type_check;
ALTER TABLE subscription_ledger ADD CONSTRAINT subscription_ledger_source_type_check
  CHECK (source_type IN ('ORDER', 'INVITE_REWARD', 'ADMIN_APPROVAL', 'SYSTEM', 'REDEEM_CODE'));

-- 默认权限会把新表的增改开放给 app。兑换明文密文和后台幂等表与 App 无关。
-- App 只按哈希查找并标记使用；行锁用事务级顾问锁，因此不需要整表 UPDATE。
REVOKE ALL ON redeem_batches, admin_redeem_mutations, redeem_card_types, redeem_codes FROM yuhai_app, PUBLIC;
-- 卡类型表没有机密，兑换时要读名称。批次表上的密文不给 App。
GRANT SELECT ON redeem_card_types TO yuhai_app;
GRANT SELECT (id, code_hash, batch_id, card_type, duration_days, tier, device_limit, traffic_bytes, status, redeemed_by, redeemed_at)
  ON redeem_codes TO yuhai_app;
GRANT UPDATE (status, redeemed_by, redeemed_at) ON redeem_codes TO yuhai_app;
REVOKE DELETE, TRUNCATE ON redeem_codes, redeem_batches, redeem_card_types FROM yuhai_admin;
REVOKE UPDATE, DELETE, TRUNCATE ON admin_redeem_mutations FROM yuhai_admin, yuhai_app, PUBLIC;
REVOKE ALL ON redeem_batches, admin_redeem_mutations FROM yuhai_readonly;
GRANT SELECT ON redeem_card_types, redeem_codes TO yuhai_readonly;

-- +goose Down
DROP TABLE admin_redeem_mutations;
DROP TABLE redeem_codes;
DROP TABLE redeem_batches;
DROP TABLE redeem_card_types;
DELETE FROM admin_role_permissions WHERE permission_code IN ('redeem_codes.read','redeem_codes.generate','redeem_codes.export','redeem_codes.revoke');
DELETE FROM admin_permissions WHERE code IN ('redeem_codes.read','redeem_codes.generate','redeem_codes.export','redeem_codes.revoke');
