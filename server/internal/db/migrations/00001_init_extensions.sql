-- +goose Up
-- +goose StatementBegin
-- =====================================================================
-- 宇海加密VPN 数据库结构 v1
-- 金额 *_micro：1 USDT = 1,000,000；流量单位：字节；时间：UTC
-- =====================================================================

CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- 只追加表保护
CREATE OR REPLACE FUNCTION forbid_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION '表 % 只允许追加，禁止 %', TG_TABLE_NAME, TG_OP;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- 不提供自动回滚：账务表回滚需人工评审后执行
SELECT 1;
-- +goose StatementEnd
