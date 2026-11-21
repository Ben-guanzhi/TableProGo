# 插件安装指南（TABLEPRO_PLUGIN 注册）

> 适用：TableProGo 的所有 `dbcore/plugindriver` 插件（含 navop-* 与
> 2026-11-21 新增的五款产品差距插件）。宿主**零改动**——安装 = 告诉
> 宿主可执行文件在哪，重启即生效。

---

## 1. 两种安装方式

### 方式 A：环境变量（最快，开发/试用）

按引擎类型声明环境变量，值为**可执行文件路径**：

```powershell
# Windows PowerShell（当前会话）
$env:TABLEPRO_PLUGIN_REPORT      = 'D:\tools\db\TableProGo\plugins\navop-report\navop-report.exe'
$env:TABLEPRO_PLUGIN_QUERYBUILDER= 'D:\tools\db\TableProGo\plugins\navop-querybuilder\navop-querybuilder.exe'
$env:TABLEPRO_PLUGIN_MONITOR     = 'D:\tools\db\TableProGo\plugins\navop-monitor\navop-monitor.exe'
$env:TABLEPRO_PLUGIN_COLLAB      = 'D:\tools\db\TableProGo\plugins\navop-collab\navop-collab.exe'
$env:TABLEPRO_PLUGIN_DEBUG       = 'D:\tools\db\TableProGo\plugins\navop-debug\navop-debug.exe'
```

```bash
# macOS / Linux
export TABLEPRO_PLUGIN_REPORT=/path/to/navop-report
```

**规则**（`internal/plugins.go` 实证）：
- 变量名 `TABLEPRO_PLUGIN_<TYPE>`，`<TYPE>` 会**转小写**作为插件类型；
- 路径可用 `;` 追加参数：`TABLEPRO_PLUGIN_<TYPE>=/path/plugin;arg1;arg2`
  （一个二进制服务多个引擎时用——consoleplugin 按首参选引擎）;
- 相对路径在注册时解析为绝对路径（子进程的 cwd 未必是输入命令的目录）;
- **同一类型重复声明会报错并拒绝**（不再静默 last-wins）;
- 路径不存在会在启动告警中列出，不阻塞主程序。

### 方式 B：注册表 JSON（长期安装，可在插件面板管理）

插件面板的「确保注册表」会创建默认注册表文件，路径由 API
`POST /api/plugins/ensure-registry` 返回。格式（`internal/pluginreg` 实证）：

```json
{
  "schema": 1,
  "plugins": [
    {
      "type": "report",
      "name": "报表设计器",
      "version": "0.1.0",
      "path": "plugins/navop-report/navop-report.exe",
      "sha256": "",
      "description": "声明式报表 → Markdown",
      "enabled": true
    }
  ]
}
```

字段要点：
- `schema` 必须是 `1`（未知值拒绝，不猜测）;
- `path` 相对注册表文件目录或绝对路径;
- `sha256` 留空 = 落入 `unknown` 桶（可运行但提示未背书）;
- **`enabled` 缺省 false**——安装与启用是两个决定，注册表不会自带引擎。

---

## 2. 信任机制（安全底线）

启动注册**先校验后运行**（`pluginreg.Verify`）：

| 状态 | 含义 | 行为 |
|---|---|---|
| `trusted` | 路径在信任清单且摘要匹配 | 运行 |
| `unknown` | 从未信任（新插件） | **运行，但面板标注未背书** |
| `tampered` | 已信任但字节变了 | **拒绝运行** |

这个不对称是刻意设计：没人背书的文件是你选择试用的插件；已背书却
变字节正是信任清单要挡的替换攻击。信任清单在用户数据目录
`plugin_trust.json`（`POST /api/plugins/trust` 管理工具）。

**给插件背书**：在插件面板点「信任」，或调用 trust API——记录路径的
SHA-256；此后二进制变动会被拒绝，直到重新信任。

---

## 3. 五款新增插件的能力与参数

| 类型（`TABLEPRO_PLUGIN_*`） | action | 参数 | 用途 |
|---|---|---|---|
| `REPORT` | `design` | title / columns(JSON数组) / rows(JSON对象数组) | 声明式报表 → Markdown |
| `QUERYBUILDER` | `build` | table / columns / where / order / limit | `sqltext.BuildSelect`（内核 v0.2） |
| `MONITOR` | `check` | metrics(JSON：name/value/threshold) | 阈值判定 + 状态着色 |
| `COLLAB` | `add` / `list` | sql / comment / author | SQL 哈希归因评论，JSONL 持久化 |
| `DEBUG` | `sessions` | 无 | **诚实边界**：Oracle debug 面前置未满足时返回 unavailable |

**调试插件特别说明**：它只声明协议骨架，不伪造调试会话——Oracle 的
`DBMS_DEBUG_JDWP` 面尚未在插件体系实现（§19697 超范围裁定的前置条件）。
安装后你会看到明确的「不可用 + 原因」，而非神秘失败。

---

## 4. 验证步骤

1. 构建插件：`cd plugins/<name>; go build -o <name>.exe .`（五个插件
   各自 `go test ./...` 全绿）;
2. 设置环境变量或填写注册表 JSON、`enabled: true`;
3. 重启 TableProGo（插件在启动时注册，`Load()` 一次）;
4. 打开**插件面板**核对：类型、信任状态（首次为 unknown）、启用状态;
5. 在连接/对象树中找到插件提供的 action，执行一次确认返回。

**排错**：
- 面板没出现 → 检查路径是否存在、类型拼写、是否重复声明;
- 显示 tampered → 二进制在信任后被改过——重新信任或恢复原文件;
- 执行报 unknown action → 插件版本与宿主协议版本不匹配（banner 校验）。

---

## 5. 协议约定（供自建插件参考）

插件是**子进程**，走 newline-JSON 协议（`dbcore/plugindriver`）：
1. 启动先输出一行 banner：`{"proto":"dbcore-plugin","version":1,"kind":...}`;
2. 每行一个请求 `{"id":N,"method":"connListActions"|"connRunAction",...}`;
3. 每行一个响应 `{"id":N,"result":...,"error":...}`;
4. `connListActions` 返回 ConnAction 清单（宿主据此渲染原生表单），
   `connRunAction` 按 `action` + `args` 执行，结果以网格呈现。

五个新插件的 `main.go` 是最简参考（约 80-110 行），与 navop-sqlgen
同构。
