package main

import (
	"context"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"gopkg.in/yaml.v3"

	"github.com/yuhai-vpn/server/internal/config"
)

// 与 cmd/api/routes_test.go 同一个目的：让「契约里有、代码里没挂上」变成会红的测试，
// 而不是靠谁记得。后台这一面缺口更大，所以 pendingAdmin 很长——**这正是要点**：
// 这张表就是后台真实的未完成清单，每加一行都是一次公开承认，删一行才算做完一个接口。
var pendingAdmin = map[string]string{
	// 仪表盘与统计
	// 管理员与权限
	// 用户写操作
	// 双人审批链路与执行器登记表已就绪（第 25、28 节），到账异常处置已经用这条路
	// 真的动过钱。这一个还没做，卡的不是链路而是规则：方案 12.4 要求单笔上限
	// （余额 >100 USDT、时长 >90 天须超管审批）与每人每日总额上限，这两条目前
	// 没有任何存放处，也没有产品口径。不做一个没有上限的资金调整接口。
	"POST /users/{id}/adjustments": "余额与权益人工调整未实现（缺方案 12.4 的单笔/每日上限规则）",
	// App 侧 POST /v1/account/secret/reset 已实现（第 26 节），换锁原语可复用；
	// 后台代重置卡在方案 12.4 的所有权核验：一次性客服验证码需要 App 侧先有签发接口，
	// TRON 地址签名需要 secp256k1，而本仓库 vendored 依赖里没有。不做跳过核验的版本。
	"POST /users/{id}/secret-reset": "客服代重置账户密钥未实现（缺方案 12.4 的所有权核验前置条件）",
	// 后台只读（App 侧同名接口已实现，后台这一份没有）
	// 配置
	// 资金与到账
}

// 开发工具路由只在 APP_ENV≠production 且 DEV_TOOLS_ENABLED=true 时注册，
// 本测试按默认配置构建路由，所以它们不该出现。
var devOnlyAdmin = map[string]bool{
	"POST /dev/mock-deposit": true, "GET /dev/reconcile": true,
	"POST /dev/mock-traffic": true, "POST /dev/clock": true,
}

func normalizeAdminPath(p string) string {
	p = strings.TrimPrefix(p, "/admin/v1")
	p = regexp.MustCompile(`:[A-Za-z_][A-Za-z0-9_]*`).ReplaceAllString(p, "{}")
	return regexp.MustCompile(`\{[^}]*\}`).ReplaceAllString(p, "{}")
}

func mountedAdminRoutes(t *testing.T) map[string]bool {
	t.Helper()
	gin.SetMode(gin.TestMode)
	// pgxpool 是惰性连接的：这里只要一个非 nil 的池对象用于构建路由，不会真的连库。
	pool, err := pgxpool.New(context.Background(), "postgres://routes-test.invalid:5432/none?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	cfg := config.Config{Env: config.EnvDevelopment, FieldEncKeyID: "routes-test",
		FieldEncKey: make([]byte, 32)}
	r, err := buildAdminRouter(cfg, pool)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, route := range r.Routes() {
		out[route.Method+" "+normalizeAdminPath(route.Path)] = true
	}
	return out
}

func adminContractOperations(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile("../../../api/openapi/admin.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string `yaml:"operationId"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	ops := map[string]string{}
	for p, item := range doc.Paths {
		for method, op := range item {
			switch strings.ToUpper(method) {
			case "GET", "POST", "PUT", "PATCH", "DELETE":
				ops[strings.ToUpper(method)+" "+p] = op.OperationID
			}
		}
	}
	if len(ops) == 0 {
		t.Fatal("admin.yaml 里没读到任何接口")
	}
	return ops
}

func TestAdminRoutesMatchContract(t *testing.T) {
	mounted := mountedAdminRoutes(t)
	ops := adminContractOperations(t)

	var missing, stale []string
	for key, operationID := range ops {
		normalized := normalizeAdminPath(key)
		_, pending := pendingAdmin[key]
		if mounted[normalized] {
			if pending {
				stale = append(stale, key+"（路由已挂，请从 pendingAdmin 里删掉）")
			}
			continue
		}
		if pending || devOnlyAdmin[key] {
			continue
		}
		missing = append(missing, key+"  operationId="+operationID)
	}
	sort.Strings(missing)
	sort.Strings(stale)
	if len(missing) > 0 {
		t.Fatalf("契约里有、路由没挂（要么实现它，要么写进 pendingAdmin 并说明原因）：\n  %s",
			strings.Join(missing, "\n  "))
	}
	if len(stale) > 0 {
		t.Fatalf("pendingAdmin 已过期：\n  %s", strings.Join(stale, "\n  "))
	}
	for key := range pendingAdmin {
		if _, ok := ops[key]; !ok {
			t.Fatalf("pendingAdmin 里的 %q 在契约里不存在", key)
		}
	}
}

func TestAdminRoutesAreAllInContract(t *testing.T) {
	ops := adminContractOperations(t)
	inContract := map[string]bool{}
	for key := range ops {
		inContract[normalizeAdminPath(key)] = true
	}
	exempt := map[string]bool{"GET /healthz": true}

	var extra []string
	for key := range mountedAdminRoutes(t) {
		if inContract[key] || exempt[key] {
			continue
		}
		extra = append(extra, key)
	}
	sort.Strings(extra)
	if len(extra) > 0 {
		t.Fatalf("路由挂了但契约里没有（铁律：先改 api/openapi/*.yaml 再写代码）：\n  %s",
			strings.Join(extra, "\n  "))
	}
}

// 开发工具必须只在非生产且显式打开时才注册——这条比缺口清单更要紧。
func TestAdminDevToolsStayOffByDefault(t *testing.T) {
	mounted := mountedAdminRoutes(t)
	for key := range devOnlyAdmin {
		if mounted[key] {
			t.Fatalf("默认配置下不应注册开发工具路由：%s", key)
		}
	}
}
