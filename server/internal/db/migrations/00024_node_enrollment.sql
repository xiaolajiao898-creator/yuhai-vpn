-- +goose Up
-- 节点注册：一次性令牌签发（后台）与 CSR 换证书（节点侧）。
--
-- 这是把一台真实机器接进控制面的最后一步，风险等级按 L2 对待，且需要二次验证。
-- 与 nodes.write 分开定义：改节点登记信息和给节点发身份是两件事——后者等于决定
-- 「哪台机器可以代表这个节点」。
-- 两个能力分开定义，权限码与二次验证的 action_code 一一对应：
-- 「能给节点发身份」和「能把节点的身份作废」是两件事，可以只授予其中一个。
INSERT INTO admin_permissions(code,name,risk_level) VALUES
 ('nodes.enroll_token','签发节点一次性注册令牌',2),
 ('nodes.revoke','吊销节点证书绑定并下线',2)
 ON CONFLICT(code) DO NOTHING;

-- 注册记录：每次成功签发客户端证书都留一条。
--
-- 存在的理由不是流水账，而是为了能回答两个具体问题：
--   1. 这台节点当前用的证书是哪一张、什么时候签的、什么时候到期；
--   2. 节点自己报上来的 WireGuard 公钥，和后台配置的入站 public_params 是否一致——
--      不一致就是「隧道配好了但连不上」这类故障最常见的根因，必须能当场比对。
--
-- 不存私钥，也不存证书本体：指纹足以做绑定与比对，证书正本在节点手上。
CREATE TABLE node_enrollments (
  id               BIGINT       GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  node_id          BIGINT       NOT NULL REFERENCES nodes(id),
  enroll_token_id  BIGINT       NOT NULL REFERENCES node_enroll_tokens(id),
  cert_fingerprint CHAR(64)     NOT NULL CHECK (cert_fingerprint ~ '^[a-f0-9]{64}$'),
  cert_not_after   TIMESTAMPTZ  NOT NULL,
  wg_public_key    VARCHAR(64)  NOT NULL,
  reality_public_key VARCHAR(64) NOT NULL,
  agent_version    VARCHAR(32)  NOT NULL,
  enrolled_at      TIMESTAMPTZ  NOT NULL,
  UNIQUE (enroll_token_id)
);

CREATE INDEX node_enrollments_node_idx ON node_enrollments (node_id, enrolled_at DESC);

-- 后台节点 L2 操作的幂等记录。与 admin_node_mutations 分开：那张表是 L1 的，
-- 这里的每一条都对应一次二次验证，混在一起会让「哪些操作做过二次验证」变得不清楚。
--
-- response 允许为空：注册令牌**不做响应重放**（明文只存在于首次响应里），
-- 重复提交按 40901 拒绝，所以这里对它只记录「发生过」，不记录内容。
CREATE TABLE admin_node_security_mutations (
  admin_user_id BIGINT      NOT NULL REFERENCES admin_users(id),
  operation_id  UUID        NOT NULL,
  action_code   TEXT        NOT NULL CHECK (action_code IN ('nodes.enroll_token','nodes.revoke')),
  request_hash  CHAR(64)    NOT NULL CHECK (request_hash ~ '^[a-f0-9]{64}$'),
  node_id       BIGINT      NOT NULL REFERENCES nodes(id),
  session_id    BIGINT      NOT NULL,
  proof_hash    CHAR(64)    NOT NULL CHECK (proof_hash ~ '^[a-f0-9]{64}$'),
  created_at    TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (operation_id)
);

CREATE INDEX admin_node_security_mutations_node_idx ON admin_node_security_mutations (node_id, created_at);

-- +goose Down
DROP TABLE admin_node_security_mutations;
DROP TABLE node_enrollments;
