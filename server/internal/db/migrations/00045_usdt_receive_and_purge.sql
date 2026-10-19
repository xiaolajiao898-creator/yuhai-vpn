-- +goose Up
-- 收款地址不再要求离线资金密钥签名。地址只收 USDT，系统不保存、也不使用它的私钥。
-- 新地址由管理员在后台登记（见 finance.address_create），审计日志记下是谁批的。
-- 旧的签名列留着：已经签过的地址仍然可以把证明发给客户端；新地址这两列为空。
ALTER TABLE receive_addresses ALTER COLUMN signature DROP NOT NULL;
ALTER TABLE receive_addresses ALTER COLUMN sig_key_id DROP NOT NULL;
ALTER TABLE receive_addresses DROP CONSTRAINT IF EXISTS receive_addresses_signature_pair;
ALTER TABLE receive_addresses ADD CONSTRAINT receive_addresses_signature_pair
  CHECK ((signature IS NULL) = (sig_key_id IS NULL));

-- 登记收款地址是单人 L2（二次验证 + 审计）。双人审批在只有一位所有者时走不通：
-- 审批人不能是发起人。已经能启停收款地址或导入地址包的角色，一并获得登记能力。
INSERT INTO admin_permissions(code,name,risk_level) VALUES
 ('finance.address_create','登记收款地址（管理员审批，不保存私钥）',2)
 ON CONFLICT(code) DO NOTHING;

ALTER TABLE admin_finance_mutations DROP CONSTRAINT IF EXISTS admin_finance_mutations_action_code_check;
ALTER TABLE admin_finance_mutations ADD CONSTRAINT admin_finance_mutations_action_code_check
  CHECK (action_code IN ('finance.address_status','finance.shared_pay_write','finance.address_create'));

INSERT INTO admin_role_permissions(role_id,permission_code)
 SELECT r.id, 'finance.address_create'
   FROM admin_roles r
  WHERE r.code = 'super_admin'
 ON CONFLICT DO NOTHING;

INSERT INTO admin_role_permissions(role_id,permission_code)
 SELECT role_id, 'finance.address_create'
   FROM admin_role_permissions
  WHERE permission_code IN ('finance.address_status','finance.address_import')
 ON CONFLICT DO NOTHING;

-- ip_hash_purge 要把 30 天前的设备绑定日志里的 ip_hash 置空。
-- 这张表是只追加的：00039 收回了整表 UPDATE，触发器也会拒绝任何修改，
-- 于是这个任务每小时报一次权限错误。只放行「把 ip_hash 置为 NULL」这一件事。
-- goose 会按分号切语句，函数体必须整段执行。
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION redact_bind_log_ip() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  old_row jsonb;
  new_row jsonb;
BEGIN
  IF TG_OP <> 'UPDATE' THEN
    RAISE EXCEPTION '表 % 只允许追加，禁止 %', TG_TABLE_NAME, TG_OP;
  END IF;
  IF NEW.ip_hash IS NOT NULL OR OLD.ip_hash IS NULL THEN
    RAISE EXCEPTION '表 % 只允许追加，禁止 %', TG_TABLE_NAME, TG_OP;
  END IF;
  old_row := to_jsonb(OLD) - 'ip_hash';
  new_row := to_jsonb(NEW) - 'ip_hash';
  IF old_row IS DISTINCT FROM new_row THEN
    RAISE EXCEPTION '表 % 只允许追加，禁止 %', TG_TABLE_NAME, TG_OP;
  END IF;
  RETURN NEW;
END $$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS trg_device_bind_logs_append_only ON device_bind_logs;
CREATE TRIGGER trg_device_bind_logs_append_only
  BEFORE UPDATE OR DELETE ON device_bind_logs
  FOR EACH ROW EXECUTE FUNCTION redact_bind_log_ip();

GRANT UPDATE (ip_hash) ON device_bind_logs TO yuhai_app;
GRANT UPDATE (register_ip_hash, register_ip_prefix_hash) ON users TO yuhai_app;

-- session_purge 删除过期 90 天以上的会话。yuhai_app 原先没有 DELETE，每小时报 42501。
-- 会话不是账本。TRUNCATE 仍然不给。
GRANT DELETE ON auth_sessions TO yuhai_app;
REVOKE TRUNCATE ON auth_sessions FROM yuhai_app;

-- +goose Down
-- +goose StatementBegin
SELECT 1;
-- +goose StatementEnd
