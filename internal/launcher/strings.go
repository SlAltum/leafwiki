package launcher

import "time"

// Strings holds every user-visible text of the window (C-10). Keeping them here
// means the view files contain layout only, and a wording change touches one
// file.
type Strings struct {
	AppTitle    string
	TabBasic    string
	TabAdvanced string

	// Basic page (FR-804).
	TitleNote      string
	FindingPending string
	LogWrittenTo   string
	MoveHint       string
	OpenWiki       string
	Copy           string
	Restart        string
	Stop           string
	OpenDataDir    string
	Address        string
	Agent          string
	AgentHint      string
	Account        string
	AccountName    string
	CopyHint       string
	LogSection     string
	RefreshNow     string
	NoLogs         string
	NoMatchingRows string

	// Advanced page (FR-504/505/510).
	AdvancedTitle string
	AdvancedIntro string
	AdvancedHint  string
	ColumnName    string
	ColumnValue   string
	ColumnDefault string
	ColumnEnv     string
	Search        string
	OnlyChanged   string
	ShowAdvanced  string
	// AutoRefreshLabel is the focus-time content check switch (FR-405).
	AutoRefreshLabel string
	TestConfig       string
	Save             string
	RestartApply     string
	ReadOnly         string
	NeedsRestart     string
	ConfigOK         string
	DataDir          string

	// Status words (FR-805).
	StatusStopped  string
	StatusStarting string
	StatusRunning  string
	StatusStopping string
	StatusClosing  string
	StatusFailed   string

	// Refreshing (FR-402/403).
	RefreshRunning string
	RefreshFailed  string
	RefreshDoneAt  string

	// Failures (FR-701/704).
	FailedOpenWiki    string
	FailedOpenDir     string
	FailedCopy        string
	AnotherInstance   string
	UnwritableDataDir string
	Closing           string
	ForceQuitQuestion string
	ForceQuitDetail   string
	PasswordUnknown   string
	PasswordInitial   string
	CredentialsNote   string
	PortInUseDetail   string
	LogFileHint       string
	UpTimePrefix      string

	// GroupNotes are the one-line explanations under the advanced page's group
	// headers, keyed by the CLI's category name.
	GroupNotes map[string]string
	// GroupTitles translate the CLI's English category names for the window.
	GroupTitles map[string]string
	GroupOther  string

	// ResetAdminPassword and its outcome (FR-205).
	ResetAdminPassword string
	PasswordReset      string
	AccountSection     string
	AccountAdmin       string
	AccountNote        string
	AccountResetNote   string
}

// Text is the window's one copy source.
var Text = Strings{
	AppTitle:    "LeafWiki",
	TabBasic:    "基本",
	TabAdvanced: "高级设置",

	TitleNote:      "关掉这个窗口就会停止服务。",
	FindingPending: "磁盘上有改动还没生效",
	LogWrittenTo:   "全部写在",
	MoveHint:       "整份拷走即可搬家",
	OpenWiki:       "打开百科",
	Copy:           "复制",
	Restart:        "重启服务",
	Stop:           "停止服务",
	OpenDataDir:    "打开数据目录",
	Address:        "地址",
	Agent:          "AI 助手",
	AgentHint:      "整段复制给 AI 助手；完整规则在程序旁边的 AGENT.md。",
	Account:        "账号 / 初始密码",
	AccountName:    "admin",
	CopyHint:       "首次启动时生成。",
	LogSection:     "日志",
	RefreshNow:     "刷新内容",
	NoLogs:         "暂无日志。",
	NoMatchingRows: "没有匹配的参数。",

	AdvancedTitle:    "高级设置",
	AdvancedIntro:    "启动参数来自命令行声明。",
	AdvancedHint:     "还有变更收在「显示高级项」里",
	ColumnName:       "参数",
	ColumnValue:      "值",
	ColumnDefault:    "默认",
	ColumnEnv:        "环境变量",
	Search:           "搜索",
	OnlyChanged:      "只看改动项",
	ShowAdvanced:     "显示高级项",
	AutoRefreshLabel: "自动刷新内容",
	TestConfig:       "测试解析",
	Save:             "保存",
	RestartApply:     "重启并应用",
	ReadOnly:         "只读",
	NeedsRestart:     "保存好了。改动要重启服务才生效。",
	ConfigOK:         "配置可用。",
	DataDir:          "数据目录",

	StatusStopped:  "未运行",
	StatusStarting: "启动中…",
	StatusRunning:  "运行中",
	StatusStopping: "正在停止…",
	StatusClosing:  "正在关闭…",
	StatusFailed:   "失败",

	RefreshRunning: "正在加载新内容…",
	RefreshFailed:  "内容刷新失败。",
	RefreshDoneAt:  "已更新",

	FailedOpenWiki:     "打不开浏览器，可以手动复制上面的地址。",
	FailedOpenDir:      "打不开数据目录。",
	FailedCopy:         "复制失败，请手动选中后复制。",
	AnotherInstance:    "另一个实例正在使用这个数据目录，请稍后重试。",
	UnwritableDataDir:  "这个文件夹写不进去。请把整个程序文件夹移到可写的位置，例如桌面。",
	Closing:            "正在关闭…",
	ForceQuitQuestion:  "服务还没停下来。要强制退出吗？",
	ForceQuitDetail:    "强制退出可能丢失尚未落盘的内容。",
	PasswordUnknown:    "密码已设置（未知）",
	PasswordInitial:    "初始密码",
	CredentialsNote:    "密码以明文存放在程序文件夹里。",
	PortInUseDetail:    "端口被占用，换一个端口，或先把占用它的程序关掉。",
	LogFileHint:        "日志文件",
	UpTimePrefix:       "已运行",
	ResetAdminPassword: "重置管理员密码",
	PasswordReset:      "密码已重置，新密码已显示在基本页。",
	AccountSection:     "账号",
	AccountAdmin:       "管理员 admin",
	AccountNote:        "忘记密码时在这里重新生成。",
	AccountResetNote:   "重置后旧密码立刻失效",
	GroupOther:         "其他",
	GroupTitles: map[string]string{
		"Server":                       "服务",
		"Logging":                      "日志",
		"Authentication":               "认证",
		"Admin bootstrap":              "管理员账号",
		"Reverse-proxy authentication": "反向代理认证",
		"External URLs":                "外部地址",
		"Frontend":                     "前端",
		"Features":                     "功能",
		"Revisions":                    "修订历史",
		"Metrics":                      "指标",
		"Git backup":                   "Git 备份",
		"Snapshots":                    "快照",
		"Snapshots & restore":          "快照与恢复",
		"Email (SMTP)":                 "邮件",
		"Semantic search":              "语义检索",
	},
	// No per-group essays: the section title and the rows say what the group is,
	// and "restart to apply" is already said once by NeedsRestart.
	GroupNotes: map[string]string{},
}

// StatusWord maps a state to its fixed label (FR-805).
func StatusWord(name string) string {
	switch name {
	case "starting":
		return Text.StatusStarting
	case "running":
		return Text.StatusRunning
	case "stopping":
		return Text.StatusStopping
	case "closing":
		return Text.StatusClosing
	case "failed":
		return Text.StatusFailed
	default:
		return Text.StatusStopped
	}
}

// UpdatedAt renders "已更新 hh:mm".
func UpdatedAt(t time.Time) string {
	return Text.RefreshDoneAt + " " + t.Local().Format("15:04")
}

// UpTime renders "已运行 2 小时 14 分". Minutes and hours only: the window is
// not a stopwatch.
func UpTime(d time.Duration) string {
	if d < time.Minute {
		return Text.UpTimePrefix + " 不到 1 分钟"
	}
	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60
	switch {
	case hours == 0:
		return Text.UpTimePrefix + " " + itoa(minutes) + " 分钟"
	case minutes == 0:
		return Text.UpTimePrefix + " " + itoa(hours) + " 小时"
	default:
		return Text.UpTimePrefix + " " + itoa(hours) + " 小时 " + itoa(minutes) + " 分"
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}
