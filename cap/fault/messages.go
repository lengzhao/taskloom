package fault

// messages is the one place a code's human text lives: HTTP errors, MCP tool
// errors and the snapshot's Blocked reasons all read it, so a reason reads the
// same wherever it appears.
var messages = map[string]string{
	CodeIdempotencyKeyRequired: "写入请求缺少 Idempotency-Key 请求头",
	CodeIdempotencyKeyReused:   "同一 Idempotency-Key 已用于不同的请求内容",
	CodeUnknownField:           "请求包含未知字段",
	CodeMalformedBody:          "请求体不是有效的 JSON 对象",
	CodeRequired:               "缺少必填字段",
	CodeInvalid:                "字段取值无效",
	CodeBasisChanged:           "任务要求已更新，请按新要求重新开始",
	CodeRoundChanged:           "任务已重开，请刷新",
	CodeVersionChanged:         "内容已被他人修改，请刷新后重试",
	CodeTaskNotActive:          "任务已结束或已取消",
	"body_too_large":           "请求体超过上限",
	"too_long":                 "内容超过长度上限",
	"resync_required":          "变化记录已过期，请重新读取任务列表并从新游标补拉",

	// Work on a task (cap/workflow reasons and command conflicts).
	"business_credential_required": "需要 Agent 接入凭据",
	"not_assigned":                 "任务未分配给你",
	"execution_in_progress":        "你已在处理，直接继续即可",
	"step_not_open":                "还不能开始：先补充任务目标",
	"not_your_execution":           "这不是你正在进行的处理",
	"execution_fenced":             "你的处理已被取消或转交，不能再提交",
	"not_reviewer":                 "只有负责人可以验收",
	"required_children_pending":    "还有必需子任务未完成",
	"result_changed":               "结果已被替换，请刷新后验收最新结果",
	"question_closed":              "问题已回答或已关闭",
	"owner_changed":                "负责人已变化，请刷新",
	"parent_changed":               "子任务关系已变化，请刷新",
	"parent_newer":                 "子任务须比父任务创建得晚",
	"not_cancelled":                "只有已取消的任务可以重开",
	"relation_needs_parent":        "Required 只用于子任务，须同时给出 ParentID",

	// Files and links.
	"too_many_files": "附件数量超过上限",
	"too_many_links": "链接数量超过上限",
	"invalid_link":   "链接须为 http/https 地址",
	"invalid_name":   "文件名为空或包含不允许的字符",

	// Members, identities and projects.
	"member_inactive":        "该成员不存在或已停用",
	"owner_not_person":       "负责人须是有效用户（不能是 Agent）",
	"not_a_person":           "只有用户有密码",
	"weak_password":          "密码须为 8–128 字节",
	"wrong_password":         "当前密码不正确",
	"invalid_subject":        "标识为空、过长或包含不允许的字符",
	"invalid_expiry":         "有效期须在现在之后、360 天以内",
	"too_many_members":       "成员数量超过上限",
	"project_archived":       "项目已归档，不能新增或移入任务",
	"owner_ambiguous":        "无法确定负责人，请指定 Owner",
	"invalid_name_or_detail": "名称为空，或名称、描述过长",

	// Tags.
	"tag_name_taken":     "已有同名标签（不区分大小写）",
	"too_many_tags":      "空间的标签数量已达上限",
	"tag_unknown":        "标签不存在",
	"tag_archived":       "标签已归档，不能再加到任务上",
	"too_many_task_tags": "一个任务的标签数量超过上限",
}

// Message is the human text of a stable code, or "" when it has none.
func Message(code string) string { return messages[code] }
