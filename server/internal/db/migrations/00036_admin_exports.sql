-- +goose Up
-- 数据导出（admin.yaml POST /exports、GET /exports、POST /exports/{id}/download）。
--
-- 方案 12.4「敏感信息展示」对导出提了三条硬要求：导出行为需 L2、导出文件带水印
-- （管理员账号 + 时间）、文件 24 小时后自动删除。这三条在库这一层就落死：
-- 水印是 NOT NULL 列（没有水印就存不进来），过期时间是 NOT NULL 列，
-- 而清理由 worker 按 expires_at 执行。
--
-- 文件存在 bytea 里，不引入对象存储。理由：导出件有 24 小时保留期、体量被硬上限卡住，
-- 为它单开一套存储和一套凭证轮换，风险比收益大；放在同一个库里，
-- 「24 小时后必须消失」才是一条能被测试钉死的事实，而不是另一个系统的承诺。
--
-- 三个能力分开定义：
--   exports.read     (L0) —— 看有哪些导出、生成到哪一步。不含任何文件内容。
--   exports.create   (L2) —— 发起一次导出。
--   exports.download (L2) —— 取回文件。**这才是数据真正离开系统的那一刻**，
--                            所以它和「发起」是两个能力：可以让人发起导出而不能自己取走，
--                            也可以让审计岗只取走而不能发起。
-- 迁移只定义能力，不授予任何角色或管理员。
INSERT INTO admin_permissions(code,name,risk_level) VALUES
 ('exports.read','查看导出任务',0),
 ('exports.create','发起数据导出',2),
 ('exports.download','下载导出文件',2)
 ON CONFLICT(code) DO NOTHING;

CREATE TABLE admin_exports (
  id             BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  kind           TEXT        NOT NULL CHECK (kind IN ('ORDERS','CHAIN_TRANSFERS','TRAFFIC','RECONCILE')),
  -- 区间对三种周期类导出是必填，对 RECONCILE 必须为空：对账是「此刻的异常清单」，
  -- 不是某段时间的流水，给它一个区间只会让人以为筛过了。CHECK 把这条钉在库里。
  range_from     DATE,
  range_to       DATE,
  CHECK ((kind = 'RECONCILE') = (range_from IS NULL)),
  CHECK ((range_from IS NULL) = (range_to IS NULL)),
  CHECK (range_from IS NULL OR range_from <= range_to),

  requested_by   BIGINT      NOT NULL REFERENCES admin_users(id),
  -- 水印按方案 12.4 = 管理员账号 + 时间。NOT NULL：没有水印的导出件不允许存在。
  watermark      TEXT        NOT NULL CHECK (btrim(watermark) <> ''),

  status         TEXT        NOT NULL DEFAULT 'PENDING'
                 CHECK (status IN ('PENDING','READY','FAILED','EXPIRED')),
  row_count      BIGINT,
  byte_size      BIGINT,
  content_sha256 CHAR(64)    CHECK (content_sha256 ~ '^[a-f0-9]{64}$'),
  content        BYTEA,
  -- 失败原因原样留给运维看（例如「超过行数上限，请缩小区间」）。
  failure_reason TEXT,

  created_at     TIMESTAMPTZ NOT NULL,
  finished_at    TIMESTAMPTZ,
  expires_at     TIMESTAMPTZ,
  -- 只要还是 READY，文件、校验和与到期时间就必须同时在；EXPIRED 之后内容必须已经没了。
  CHECK (status <> 'READY'  OR (content IS NOT NULL AND content_sha256 IS NOT NULL
                                AND expires_at IS NOT NULL AND row_count IS NOT NULL)),
  CHECK (status <> 'EXPIRED' OR content IS NULL),
  CHECK (status <> 'FAILED'  OR (content IS NULL AND failure_reason IS NOT NULL))
);
CREATE INDEX admin_exports_pending_idx ON admin_exports (created_at) WHERE status = 'PENDING';
CREATE INDEX admin_exports_sweep_idx   ON admin_exports (expires_at) WHERE status = 'READY';
CREATE INDEX admin_exports_list_idx    ON admin_exports (created_at DESC, id DESC);

-- 发起导出的幂等记录。形状与 admin_finance_mutations 等相同；分表理由也相同。
-- 下载**不在**这里记账：重复下载同一份文件本来就允许，它没有幂等语义可言，
-- 而把 CSV 再塞一份进幂等表只是白白多一份副本。下载的留痕靠审计日志。
CREATE TABLE admin_export_mutations (
  admin_user_id BIGINT      NOT NULL REFERENCES admin_users(id),
  operation_id  UUID        NOT NULL,
  action_code   TEXT        NOT NULL CHECK (action_code IN ('exports.create')),
  request_hash  CHAR(64)    NOT NULL CHECK (request_hash ~ '^[a-f0-9]{64}$'),
  target_id     BIGINT      NOT NULL CHECK (target_id > 0),
  session_id    BIGINT      NOT NULL,
  proof_hash    CHAR(64)    NOT NULL CHECK (proof_hash ~ '^[a-f0-9]{64}$'),
  -- 用 JSON 而非 JSONB：幂等重放必须返回与首次逐字节相同的响应体。
  response      JSON        NOT NULL,
  created_at    TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (operation_id)
);
CREATE INDEX admin_export_mutations_admin_idx ON admin_export_mutations (admin_user_id, created_at);

-- 后台进程读写导出任务；生成与清理由 worker（业务角色）执行。
GRANT SELECT, INSERT, UPDATE ON admin_exports TO yuhai_admin, yuhai_app;
GRANT SELECT, INSERT ON admin_export_mutations TO yuhai_admin;
-- 保留期一到整行删掉，而不是留一具空壳堆到天荒地老。
GRANT DELETE ON admin_exports TO yuhai_app;
REVOKE TRUNCATE ON admin_exports, admin_export_mutations FROM yuhai_admin, yuhai_app;
GRANT SELECT ON admin_exports, admin_export_mutations TO yuhai_readonly;

-- +goose Down
DROP TABLE admin_export_mutations;
DROP TABLE admin_exports;
DELETE FROM admin_permissions WHERE code IN ('exports.read','exports.create','exports.download');
