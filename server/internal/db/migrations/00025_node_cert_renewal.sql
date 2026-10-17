-- +goose Up
-- 节点证书续期（POST /node/v1/cert/renew）。
--
-- 续期换新证书，指纹必然改变，因此控制面必须跟着换绑。但换绑有一个很实际的风险：
-- 节点发出请求、控制面已换绑，而响应在回程丢了——节点手里还是旧证书，从此再也连不上，
-- 只能由人工重新签发注册令牌。为此保留一个**重叠窗口**：
--
--   agent_cert_prev_fingerprint  上一张证书的指纹
--   agent_cert_prev_until        该指纹还能被接受到什么时候
--
-- 窗口内两张证书都认。这不是放松校验：旧证书本来就是这台节点自己的，真被窃取的话
-- 攻击者在续期前同样持有它。吊销会把两个指纹一起清掉。
ALTER TABLE nodes
  ADD COLUMN agent_cert_prev_fingerprint CHAR(64)
    CHECK (agent_cert_prev_fingerprint IS NULL OR agent_cert_prev_fingerprint ~ '^[a-f0-9]{64}$'),
  ADD COLUMN agent_cert_prev_until TIMESTAMPTZ;

-- 两列必须同时有值或同时为空，避免出现「有旧指纹但没有截止时间」这种永久有效的残留。
ALTER TABLE nodes ADD CONSTRAINT nodes_prev_cert_pair
  CHECK ((agent_cert_prev_fingerprint IS NULL) = (agent_cert_prev_until IS NULL));

-- 续期同样记一条签发记录，但它没有注册令牌。
ALTER TABLE node_enrollments ALTER COLUMN enroll_token_id DROP NOT NULL;
ALTER TABLE node_enrollments ADD COLUMN kind TEXT NOT NULL DEFAULT 'ENROLL'
  CHECK (kind IN ('ENROLL','RENEW'));
-- 注册必须带令牌，续期必须不带——防止两条路径互相冒充。
ALTER TABLE node_enrollments ADD CONSTRAINT node_enrollments_kind_token
  CHECK ((kind = 'ENROLL') = (enroll_token_id IS NOT NULL));

-- +goose Down
ALTER TABLE node_enrollments DROP CONSTRAINT node_enrollments_kind_token;
ALTER TABLE node_enrollments DROP COLUMN kind;
ALTER TABLE nodes DROP CONSTRAINT nodes_prev_cert_pair;
ALTER TABLE nodes DROP COLUMN agent_cert_prev_until, DROP COLUMN agent_cert_prev_fingerprint;
