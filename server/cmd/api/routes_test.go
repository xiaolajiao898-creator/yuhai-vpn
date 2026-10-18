package main

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"

	"github.com/yuhai-vpn/server/internal/account"
	"github.com/yuhai-vpn/server/internal/auth"
	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/config"
)

// 这个测试守的是一类很安静的缺陷：契约里写了接口，代码里没挂上，谁也不报错。
// PUT /v1/devices/current/wg-key 就这么漏了——契约早就有，但没有任何代码会写
// device_credentials.wg_pubkey，于是控制面清单永远是空的，连接材料永远发不出来，
// 而所有测试都是绿的。反向同样要守：路由挂了契约里没有，就是绕过了「先改契约」的铁律。
//
// pendingApp 是**明示**的未实现清单。往里加一条等于公开承认「这个接口还没做」，
// 这正是我们要的效果：缺口必须是可见的，而不是靠谁记得。
var pendingApp = map[string]string{
	"GET /v1/announcements":    "公告功能未实现",
	"POST /v1/connect/quality": "连接质量上报未实现：上报什么、留存多久需产品先定，不先写一个空收集器",
}

// placeholderApp 是「路由挂了，但只返回 40401」的占位接口。单看路由表它们是绿的，
// 所以必须单独点名——否则占位会被当成已实现。测试会反过来确认它们确实还挂着。
var placeholderApp = map[string]string{
	"GET /v1/bootstrap": "占位返回 40401；引导配置内容待定",
}

func normalizePath(p string) string {
	// gin 用 :name，OpenAPI 用 {name}；参数名不同不算差异，位置相同即可。
	p = regexp.MustCompile(`:[A-Za-z_][A-Za-z0-9_]*`).ReplaceAllString(p, "{}")
	return regexp.MustCompile(`\{[^}]*\}`).ReplaceAllString(p, "{}")
}

func mountedRoutes(t *testing.T) map[string]bool {
	t.Helper()
	gin.SetMode(gin.TestMode)
	// 只注册路由，不发请求，所以依赖可以为零值：Mount 不接触数据库。
	cfg := config.Config{Env: config.EnvDevelopment}
	mw := &auth.Middleware{}
	r := buildRouter(routerDeps{Config: cfg, Clock: clock.Real{}, MW: mw,
		Account: &account.Service{Repo: &account.Repo{}, Clock: clock.Real{}}})
	out := map[string]bool{}
	for _, route := range r.Routes() {
		out[route.Method+" "+normalizePath(route.Path)] = true
	}
	return out
}

type openAPIDoc struct {
	Paths map[string]map[string]struct {
		OperationID string `yaml:"operationId"`
	} `yaml:"paths"`
}

func contractOperations(t *testing.T, path string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc openAPIDoc
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	ops := map[string]string{}
	for p, item := range doc.Paths {
		for method, op := range item {
			switch strings.ToUpper(method) {
			case "GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS":
				ops[strings.ToUpper(method)+" "+p] = op.OperationID
			}
		}
	}
	if len(ops) == 0 {
		t.Fatalf("%s 里没读到任何接口", path)
	}
	return ops
}

func TestAppRoutesMatchContract(t *testing.T) {
	mounted := mountedRoutes(t)
	ops := contractOperations(t, "../../../api/openapi/app.yaml")

	var missing, stale []string
	for key, operationID := range ops {
		if mounted[normalizePath(key)] {
			if _, pending := pendingApp[key]; pending {
				stale = append(stale, key+"（路由已挂，请从 pendingApp 里删掉）")
			}
			continue
		}
		if _, pending := pendingApp[key]; pending {
			continue
		}
		if _, placeholder := placeholderApp[key]; placeholder {
			stale = append(stale, key+"（占位路由不见了，placeholderApp 与实际不符）")
			continue
		}
		missing = append(missing, key+"  operationId="+operationID)
	}
	for key := range placeholderApp {
		if !mounted[normalizePath(key)] {
			stale = append(stale, key+"（placeholderApp 声称已挂占位，实际没有）")
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	if len(missing) > 0 {
		t.Fatalf("契约里有、路由没挂（要么实现它，要么写进 pendingApp 并说明原因）：\n  %s",
			strings.Join(missing, "\n  "))
	}
	if len(stale) > 0 {
		t.Fatalf("pendingApp 已过期：\n  %s", strings.Join(stale, "\n  "))
	}

	// 两张表里都不该留下契约里根本没有的条目。
	for name, table := range map[string]map[string]string{"pendingApp": pendingApp, "placeholderApp": placeholderApp} {
		for key := range table {
			if _, ok := ops[key]; !ok {
				t.Fatalf("%s 里的 %q 在契约里不存在", name, key)
			}
		}
	}
	// 同一个接口不能既算未挂载又算占位。
	for key := range pendingApp {
		if _, dup := placeholderApp[key]; dup {
			t.Fatalf("%q 同时出现在 pendingApp 与 placeholderApp", key)
		}
	}
}

func TestAppRoutesAreAllInContract(t *testing.T) {
	ops := contractOperations(t, "../../../api/openapi/app.yaml")
	inContract := map[string]bool{}
	for key := range ops {
		inContract[normalizePath(key)] = true
	}
	// 不走契约的运维端点，逐条列清楚，不留「反正是内部的」这种含糊地带。
	exempt := map[string]bool{"GET /healthz": true, "GET /app/version": true}

	var extra []string
	for key := range mountedRoutes(t) {
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
