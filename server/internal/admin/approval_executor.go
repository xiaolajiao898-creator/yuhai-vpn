package admin

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// 审批执行器登记表。
//
// 在这之前，`Decide` 的通过分支里是一段写死的 if：动作不是 app_versions.publish
// 就返回「该审批动作尚未接入执行器」。契约里另外还有三个 L3 动作等着接进来
// （地址包导入、到账异常处置、新建管理员），再往那段 if 里塞第二个分支就该停手了。
//
// 登记表里的每一项声明三件事：
//
//   - targetType：审批单的 target_type 必须与之相符。**不能只认 action_code**——
//     否则一张 action_code 对得上、target_type 被改过的审批单会被喂给错误的执行器。
//   - level：执行前要对**发起人与审批人双方**重新锁定并核验的权限等级。
//     两个人都必须仍然持有这个动作的权限，任一方被撤权就不许执行。
//   - execute：在决策事务内执行。返回错误即整笔回滚，审批单不会变成 EXECUTED。
//
// 执行器只在「通过」时调用；驳回不走这里。
type approvalExecutor struct {
	targetType string
	level      int
	execute    func(ctx context.Context, s *ApprovalService, tx pgx.Tx, p ApprovalProof,
		a Approval, approvalID int64, now time.Time) error
}

// approvalExecutors 按 action_code 索引。新增 L3 动作时在这里登记一项，
// 并同步在 present() 里让 ExecutorSupported 能识别它。
var approvalExecutors = map[string]approvalExecutor{
	actionAdminCreate: {targetType: adminManagementTarget, level: 3, execute: executeAdminManagement},
	actionAdminRoles:  {targetType: adminManagementTarget, level: 3, execute: executeAdminManagement},
	publishAction:     {targetType: "app_version", level: 3, execute: executePublish},
	actionAddressImport: {targetType: addressImportTarget, level: 3,
		execute: executeAddressImport},
	actionExceptionResolve: {targetType: exceptionResolveTarget, level: 3,
		execute: executeExceptionResolve},
}

// executorFor 返回与这张审批单相符的执行器。
// action_code 与 target_type 必须同时对上——只认其一等于把执行器当成可以随便换的开关。
func executorFor(actionCode, targetType string) (approvalExecutor, bool) {
	e, ok := approvalExecutors[actionCode]
	if !ok || e.targetType != targetType {
		return approvalExecutor{}, false
	}
	return e, true
}
