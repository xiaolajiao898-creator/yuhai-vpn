-- +goose Up
-- worker 的 account_secret_purge 任务每分钟做两件事：把过期幂等记录里的响应
-- 置空（其中含加密的账户密钥），再删掉 24 小时以上的整行。
--
-- 第二件事从来没成功过。worker 和 api 一样读 DATABASE_URL，也就是以 yuhai_app
-- 连库，而 api_idempotency 上 yuhai_app 只有 arw（SELECT/INSERT/UPDATE），
-- 没有 DELETE。于是那条 DELETE 每分钟报一次 42501，任务整体被记成失败，
-- 而表只增不减。置空那一步是独立的 Exec，先提交了，所以密钥并没有泄留——
-- 但这个任务从写下来的那天起就没有一次跑完过，只是没有任何测试以业务角色
-- 调用过它。POST /v1/account/secret/reset 的清理测试把它撞了出来。
--
-- 幂等记录是有保留期的运行数据，不是账本：给 yuhai_app 补上 DELETE 是本来就
-- 该有的授权，而不是为了让测试变绿而放宽权限。附近的 TRUNCATE 仍然不给。
GRANT DELETE ON api_idempotency TO yuhai_app;
REVOKE TRUNCATE ON api_idempotency FROM yuhai_app;

-- +goose Down
REVOKE DELETE ON api_idempotency FROM yuhai_app;
