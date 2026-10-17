package account

import (
	"context"
	"time"
)

// Jobs 由 worker 定时调用；全部为幂等的批量清理。
type Jobs struct{ Repo *Repo }

// PurgeAccountSecrets 清空幂等记录里含账户密钥的响应，并删除 24 小时以上的记录。
//
// 有两个接口会把明文账户密钥（整体加密后）放进 api_idempotency.response：注册与
// 密钥重置。两者的展示窗口各自成立，所以这里按 endpoint 分别算截止时间——
// 合成一个常量的话，哪天只改了一边的 TTL，另一边就会静悄悄地多留一阵子。
func (j *Jobs) PurgeAccountSecrets(ctx context.Context, now time.Time) error {
	if _, err := j.Repo.Pool.Exec(ctx, `
		UPDATE api_idempotency SET response = NULL
		 WHERE response IS NOT NULL
		   AND ( (user_id = $1 AND endpoint = 'register'     AND created_at < $2)
		      OR (                 endpoint = 'secret_reset' AND created_at < $3) )`,
		idemUserRegister, now.Add(-idemRegisterTTL), now.Add(-idemSecretResetTTL)); err != nil {
		return err
	}
	_, err := j.Repo.Pool.Exec(ctx, `DELETE FROM api_idempotency WHERE created_at < $1`, now.Add(-24*time.Hour))
	return err
}

// PurgeIPHashes 注册 IP 哈希保留 30 天（方案 12.6 保留期表）。
func (j *Jobs) PurgeIPHashes(ctx context.Context, now time.Time) error {
	_, err := j.Repo.Pool.Exec(ctx, `
		UPDATE users SET register_ip_hash = NULL, register_ip_prefix_hash = NULL
		 WHERE created_at < $1 AND (register_ip_hash IS NOT NULL OR register_ip_prefix_hash IS NOT NULL)`, now.Add(-30*24*time.Hour))
	if err != nil {
		return err
	}
	_, err = j.Repo.Pool.Exec(ctx, `UPDATE device_bind_logs SET ip_hash = NULL WHERE created_at < $1 AND ip_hash IS NOT NULL`, now.Add(-30*24*time.Hour))
	return err
}

// PurgeExpiredSessions 删除过期 90 天以上的会话记录。
func (j *Jobs) PurgeExpiredSessions(ctx context.Context, now time.Time) error {
	_, err := j.Repo.Pool.Exec(ctx, `DELETE FROM auth_sessions WHERE expires_at < $1`, now.Add(-90*24*time.Hour))
	return err
}
