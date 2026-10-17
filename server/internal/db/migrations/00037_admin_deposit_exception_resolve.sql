-- +goose Up
-- 到账异常处置（admin.yaml POST /deposit-exceptions/{id}/resolve）。
--
-- 在这之前，deposit_exceptions 只能看不能动：Settle 匹配不上就把转账丢进这张表，
-- 而**队列里的钱没有任何出路**。金额差一点、付款晚了几分钟、同一个地址被付了两次，
-- 转账就停在 EXCEPTION 上，用户的钱却已经到了我们的地址。
--
-- 这是第一个真正动别人的钱的后台能力，因此是 L3：发起人之外还要另一名有同样权限的
-- 管理员审批通过，钱才在执行器里动（internal/admin 的 executeExceptionResolve）。
-- 迁移只定义能力，不授予任何角色或管理员。
INSERT INTO admin_permissions(code,name,risk_level) VALUES
 ('finance.deposit_exception','处置到账异常（转入订单/转入余额/驳回）',3)
 ON CONFLICT(code) DO NOTHING;

-- deposit_exceptions.resolution_note 与 approval_id 早就在表里（00005），一直没有写入方。
-- 这一节补上写入方，同时把两条本该成立的约束钉死：
--   1. 处置过的异常必须有结论时间；
--   2. 处置过的异常必须指向一张审批单——没有审批单的处置，事后查不出是谁批的。
-- 用 NOT VALID 添加：老库里若存在历史遗留行（本项目尚无，但这条迁移要能在任何库上跑），
-- 不该因为它们而卡住新约束；之后由 DBA 择机 VALIDATE。
ALTER TABLE deposit_exceptions
  ADD CONSTRAINT deposit_exceptions_resolved_shape
  CHECK ((status = 'OPEN') = (resolved_at IS NULL)) NOT VALID;
ALTER TABLE deposit_exceptions
  ADD CONSTRAINT deposit_exceptions_resolved_approval
  CHECK (status = 'OPEN' OR approval_id IS NOT NULL) NOT VALID;

-- 权限这一节**不需要新授**：处置要改的 deposit_exceptions / orders / chain_transfers，
-- 要写的 balance_ledger / subscription_ledger / user_balances / outbox_events，
-- yuhai_admin 早在第 18 节（邀请奖励复核）之后就已经全部持有。
-- 这里明写出来，是为了让「这一轮没有扩大数据库权限」成为一句可核对的话，
-- 而不是靠谁记得。要收紧的是能力（admin_permissions），不是表权限。

-- +goose Down
ALTER TABLE deposit_exceptions DROP CONSTRAINT deposit_exceptions_resolved_approval;
ALTER TABLE deposit_exceptions DROP CONSTRAINT deposit_exceptions_resolved_shape;
DELETE FROM admin_permissions WHERE code = 'finance.deposit_exception';
