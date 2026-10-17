// admin-init：生产环境开通管理员账号（仅在服务器上由 root 执行）。
//
// 后台不内置默认管理员；第一个账号只能用本工具在服务器本机开通。
// 用户名已存在时拒绝，绝不覆盖或重置已有账号。
// 密码与两步验证密钥随机生成，只打印一次，不写入任何日志或文件。
//
//	admin-init -username boss            # 开通超级管理员（全部权限，不含开发工具）
//	admin-init -username boss -reset-password   # 已有账号重设密码（两步验证不变，旧登录全部失效）
//
// 连接串读 MIGRATE_DATABASE_URL（表所有者），加密密钥读 FIELD_ENC_KEY_HEX / FIELD_ENC_KEY_ID。
package main

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"regexp"

	"github.com/jackc/pgx/v5"
	"github.com/yuhai-vpn/server/internal/admin"
	"github.com/yuhai-vpn/server/internal/fieldenc"
)

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "错误："+format+"\n", a...)
	os.Exit(1)
}

func randomPassword(n int) string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789"
	b := make([]byte, n)
	for i := range b {
		v, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			fail("随机数生成失败")
		}
		b[i] = alphabet[v.Int64()]
	}
	return string(b)
}

func main() {
	username := flag.String("username", "", "管理员用户名（3-32 位字母数字下划线）")
	issuer := flag.String("issuer", "YuhaiVPN", "验证器里显示的名称")
	resetPassword := flag.Bool("reset-password", false, "为已有账号重设密码（保留两步验证）")
	flag.Parse()
	if !regexp.MustCompile(`^[A-Za-z0-9_]{3,32}$`).MatchString(*username) {
		fail("用户名需为 3-32 位字母、数字或下划线")
	}
	key, err := hex.DecodeString(os.Getenv("FIELD_ENC_KEY_HEX"))
	if err != nil || len(key) != 32 {
		fail("FIELD_ENC_KEY_HEX 未设置或格式不对")
	}
	enc, err := fieldenc.New(os.Getenv("FIELD_ENC_KEY_ID"), key)
	if err != nil {
		fail("加密密钥初始化失败：%v", err)
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, os.Getenv("MIGRATE_DATABASE_URL"))
	if err != nil {
		fail("连接数据库失败：%v", err)
	}
	defer conn.Close(ctx)

	password := randomPassword(20)
	hash, err := admin.HashPassword(password)
	if err != nil {
		fail("密码处理失败：%v", err)
	}
	if *resetPassword {
		tx, err := conn.Begin(ctx)
		if err != nil {
			fail("开启事务失败：%v", err)
		}
		defer tx.Rollback(ctx)
		var id int64
		err = tx.QueryRow(ctx, `UPDATE admin_users SET password_hash=$2, failed_attempts=0, locked_until=NULL, updated_at=now()
 WHERE username=$1 RETURNING id`, *username, hash).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			fail("用户名 %s 不存在", *username)
		} else if err != nil {
			fail("重设失败：%v", err)
		}
		if _, err = tx.Exec(ctx, `UPDATE admin_sessions SET revoked_at=now() WHERE admin_user_id=$1 AND revoked_at IS NULL`, id); err != nil {
			fail("注销旧登录失败：%v", err)
		}
		if err = tx.Commit(ctx); err != nil {
			fail("提交失败：%v", err)
		}
		fmt.Printf("USERNAME=%s\nPASSWORD=%s\n", *username, password)
		return
	}
	raw := make([]byte, 20)
	if _, err = rand.Read(raw); err != nil {
		fail("随机数生成失败")
	}
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)

	tx, err := conn.Begin(ctx)
	if err != nil {
		fail("开启事务失败：%v", err)
	}
	defer tx.Rollback(ctx)
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM admin_users WHERE username=$1)`, *username).Scan(&exists); err != nil {
		fail("查询失败：%v", err)
	}
	if exists {
		fail("用户名 %s 已存在，本工具不会覆盖已有账号", *username)
	}
	var id int64
	if err = tx.QueryRow(ctx, `INSERT INTO admin_users(username,password_hash,totp_secret_enc) VALUES($1,$2,''::bytea) RETURNING id`, *username, hash).Scan(&id); err != nil {
		fail("写入管理员失败：%v", err)
	}
	blob, err := enc.Encrypt([]byte(secret), fieldenc.AAD("admin_users", "totp_secret_enc", id))
	if err != nil {
		fail("加密失败：%v", err)
	}
	if _, err = tx.Exec(ctx, `UPDATE admin_users SET totp_secret_enc=$2 WHERE id=$1`, id, blob); err != nil {
		fail("写入两步验证失败：%v", err)
	}
	var roleID int64
	if err = tx.QueryRow(ctx, `INSERT INTO admin_roles(code,name) VALUES('super_admin','超级管理员') ON CONFLICT(code) DO UPDATE SET name=admin_roles.name RETURNING id`).Scan(&roleID); err != nil {
		fail("写入角色失败：%v", err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO admin_role_permissions(role_id,permission_code) SELECT $1,code FROM admin_permissions WHERE code<>'dev.tools' ON CONFLICT DO NOTHING`, roleID); err != nil {
		fail("授权失败：%v", err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO admin_user_roles(admin_user_id,role_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, id, roleID); err != nil {
		fail("绑定角色失败：%v", err)
	}
	if err = tx.Commit(ctx); err != nil {
		fail("提交失败：%v", err)
	}
	uri := fmt.Sprintf("otpauth://totp/%s:%s?secret=%s&issuer=%s&algorithm=SHA1&digits=6&period=30",
		url.PathEscape(*issuer), url.PathEscape(*username), secret, url.QueryEscape(*issuer))
	fmt.Printf("USERNAME=%s\nPASSWORD=%s\nOTPAUTH=%s\n", *username, password, uri)
}
