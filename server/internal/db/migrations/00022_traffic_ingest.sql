-- +goose Up
-- traffic_daily 声明为按 stat_date 范围分区，但此前没有创建任何分区，任何写入都会因
-- 「没有匹配的分区」失败。业务角色按铁律不得持有建表权限，因此分区只能由迁移建立，
-- 不能在运行时按月自动创建。
--
-- 这里建的是 DEFAULT 分区：写入永远不会因为缺分区而丢数据。
-- 代价：将来 DBA 要改为按月分区时，必须先把 default 分区中落在目标月份的行迁出，
-- 再 ATTACH 该月分区（PostgreSQL 不允许与 default 分区中已有行重叠的分区直接挂载）。
-- 方案 8.5 的「按月分区、保留 180 天」属于阶段三容量工作，届时按此步骤处理。
CREATE TABLE traffic_daily_default PARTITION OF traffic_daily DEFAULT;

-- 上报入库需要的最小权限：明细只追加与累加，不允许删除历史用量。
GRANT SELECT, INSERT, UPDATE ON traffic_daily TO yuhai_app;
GRANT SELECT, INSERT ON traffic_reports TO yuhai_app;
REVOKE DELETE, TRUNCATE ON traffic_daily, traffic_reports FROM yuhai_app;

-- +goose Down
DROP TABLE traffic_daily_default;
