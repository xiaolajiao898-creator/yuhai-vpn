-- +goose Up
-- 撤销执行回执：记录「某节点的实际运行状态中已不存在该凭据」这一事实。
--
-- 写入条件极严，只有三者同时成立才记录，绝不由排队事件、时间流逝或管理员操作推断：
--   1. 该节点最近一次心跳仍新鲜（90 秒内），且上报了非空 snapshot_checksum；
--   2. 该校验和与控制面此刻重算出的该节点公开清单校验和完全一致（即节点已收敛）；
--   3. 该凭据的最新期望状态为移除（node_user_events.effective_tier = 0），且确实不在该清单中。
--
-- 语义是「确认不存在」而非「确认执行了删除」：凭据可能本就从未下发到该节点。
-- 二者对运维的意义相同（该节点上没有这条访问凭据），但措辞不得夸大。
CREATE TABLE node_credential_receipts (
  node_id             BIGINT       NOT NULL REFERENCES nodes(id),
  credential_id       BIGINT       NOT NULL REFERENCES device_credentials(id),
  confirmed_absent_at TIMESTAMPTZ  NOT NULL,
  heartbeat_sequence  BIGINT       NOT NULL CHECK (heartbeat_sequence > 0),
  snapshot_checksum   CHAR(64)     NOT NULL CHECK (snapshot_checksum ~ '^[a-f0-9]{64}$'),
  PRIMARY KEY (node_id, credential_id)
);

CREATE INDEX node_credential_receipts_credential_idx
  ON node_credential_receipts (credential_id, confirmed_absent_at);

-- 只定义能力，不授予任何正式角色或管理员。
INSERT INTO admin_permissions(code,name,risk_level) VALUES
 ('devices.receipts','查看设备撤销在各节点上的执行回执',0)
 ON CONFLICT(code) DO NOTHING;

-- +goose Down
DROP TABLE node_credential_receipts;
