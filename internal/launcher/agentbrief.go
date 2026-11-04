package launcher

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// AgentBriefName is the file an agent reads to learn how to drive this wiki.
const AgentBriefName = "AGENT.md"

// WriteAgentBrief writes AGENT.md next to the executable: a shipped wiki already
// holds everything an agent needs (binary, data directory, password file), and the
// window panel shows the short form of the same thing as a copyable line. The file
// is rewritten on every start, so the port and the socket path in it are never
// stale.
func WriteAgentBrief(exeDir, dataDir, url string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	socket := filepath.Join(dataDir, "control.sock")
	passwordFile := filepath.Join(exeDir, "admin.password")

	body := fmt.Sprintf(briefTemplate, exe, exe, exe, exe, exe, passwordFile, exe,
		time.Now().Format("2006-01-02 15:04"), url, dataDir, socket, exe)

	path := filepath.Join(exeDir, AgentBriefName)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", AgentBriefName, err)
	}
	return nil
}

// briefTemplate is the file content. It avoids backticks so it stays a raw string,
// and it keeps every path in a placeholder.
const briefTemplate = `# 给 AI 助手

这个目录是一个 LeafWiki 文档站，用下面的命令读写页面。

## 入口

命令都用这个目录里的可执行文件；它自己找到旁边的 data 目录，不需要 --data-dir。

    %s status --json        # 能力、页面数、语义检索状态

## 常用命令

    %s page get <路径> --json
    %s page write <路径> --create --title "<标题>" --content-file <文件> --json
    %s page apply --batch <items.jsonl> --json
    %s page delete <路径> --recursive --user admin --password-file %s --json
    %s check --json         # 只读自检，不改磁盘

## 规则

- 读、新建、修改匿名即可；删除必须带 --user admin --password-file。
- 页面是 Markdown，存放在 data 目录的 root 文件夹里。服务运行时不要直接改这些文件：
  用上面的命令写，索引才跟着更新；直接改文件要在窗口里点一次“刷新内容”。
- --json 成功时输出恰好一个 JSON 对象；失败时 stdout 为空，结果看退出码：
  0 成功、2 用法、3 页面不存在、4 版本冲突、5 无权限、6 连不上、7 版本不符、8 部分成功、75 数据目录被占用。
- 服务没启动时，写入命令以离线方式执行（不记修订）；读取需要服务运行。
- 语义检索默认开启，写入的页面几秒内可被搜索到。

## 当前实例

由启动器于 %s 生成。

- 地址：%s
- 数据目录：%s
- 控制通道：%s（文件不存在说明服务已停止）
- 账号：admin，密码见同目录的 admin.password

更多命令见：%s --help
`

// launcherWriteAgentBrief is the default Deps.WriteAgentBrief.
func launcherWriteAgentBrief(exeDir, dataDir, url string) error {
	return WriteAgentBrief(exeDir, dataDir, url)
}
