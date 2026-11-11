# 定时作业（cron / 任务计划）

`tablepro-go --job=<配方.json>` 是一个**无状态命令**：读配方、连已保存的连接、按顺序执行步骤、打印结果、退出。没有常驻进程、没有内置调度器——调度交给系统（cron / systemd timer / Windows 任务计划 / CI），因为它们已经有失败重试、日志轮转和告警。

## 退出码

| 退出码 | 含义 | 调度器该做什么 |
|---|---|---|
| 0 | 所有步骤都成功 | 无事 |
| 1 | 有步骤失败（连接断了、表变了、备份工具报错） | 告警给「数据/运维」 |
| 2 | 配方本身不可用（文件读不到、JSON 坏了、字段拼错、步骤缺必填项） | 告警给「写配方的人」 |

这三者**必须分开**：一条 cron 邮件只会说「exit 1」，把配方笔误和数据库故障混为一谈，会把人送到错误的地方。所以配方在**连接任何数据库之前**就校验完，坏的配方以 2 退出。

## 配方格式

配方是 JSON，只引用**已保存的连接 ID**，不含任何口令——凭据仍只在连接存储里（`connections.json` + 系统钥匙串/DPAPI）。因此配方可以进版本库。

```json
{
  "name": "nightly",
  "steps": [
    { "kind": "rowsync", "name": "分析漂移（先只报告）",
      "rowsync": { "source": {"connId":"prod"}, "target": {"connId":"staging"}, "table": "items" } },

    { "kind": "backup", "name": "导出 prod",
      "backup": { "connId":"prod", "database":"shop", "mode":"full", "outPath":"/backup/shop.sql" } },

    { "kind": "wait", "wait": { "seconds": 5 } },

    { "kind": "transfer", "name": "结构+数据复制到 staging",
      "transfer": { "source": {"connId":"prod"}, "target": {"connId":"staging"} } },

    { "kind": "rowsync", "name": "应用行级同步",
      "rowsync": { "source": {"connId":"prod"}, "target": {"connId":"staging"},
                   "table": "items", "apply": true, "ignoreColumns": ["updated_at"] } }
  ]
}
```

步骤类型：

| kind | 作用 | 必填 |
|---|---|---|
| `backup` | 原生工具备份（pg_dump / mysqldump / sqlite3） | `connId`、`outPath` |
| `transfer` | 整表结构与数据复制（W5） | `source`、`target` |
| `rowsync` | 行级同步（W6）。`apply:false` 只报告漂移，不写 | `source`、`target`、`table` |
| `wait` | 暂停（0–3600 秒） | `seconds` |

`rowsync` 的关键区别：

- **`apply: false`（或不写）**：只分析、报告「新增 N / 修改 N / 删除 N」，**不碰目标**，且**漂移不算失败**。这是安全的第一步——先观察一周漂移量，再决定要不要写。
- **`apply: true`**：应用计划。不写 `seqs` 时应用**全部**变更；计划超过单次上限（预览 200 条）时**拒绝执行**而不是只应用一部分——只应用一部分会「成功」地留下没同步的数据。

## 无人值守的确认语义

界面里 transfer / rowsync 都要二次确认。作业**没有界面**，所以：**配方即确认**。把步骤写进文件并排进调度，就是操作者的批准。风险面在文件（谁能改它），不在运行期。

因此配方文件应当：

- 权限收紧（只让调度账号可读）；
- 进版本库评审，改动走 PR；
- 不要从不可信来源拷贝。

## 先干跑

正式排期前先跑 `--dry-run`：它校验配方、连库、对 transfer/rowsync **读取并报告计划**（两者本来就是只读分析），**不写任何数据**。

```bash
tablepro-go --config=~/.tablepro/connections.json --job=nightly.json --dry-run
```

## cron

```cron
# 每天 02:30，输出追加到日志；失败时 exit 1 会触发 cron 的邮件/告警
30 2 * * * /opt/tablepro/tablepro-go --config=/etc/tablepro/connections.json --job=/etc/tablepro/nightly.json >> /var/log/tablepro-nightly.log 2>&1
```

`--config` 指向**调度账号**能读到的连接文件。若那份文件是用系统钥匙串封印的，作业要以**同一个 OS 账号**运行，否则口令解不开（这是有意的：密钥跟着账号走）。

## systemd timer

```ini
# /etc/systemd/system/tablepro-nightly.service
[Unit]
Description=TablePro nightly sync
After=network-online.target

[Service]
Type=oneshot
User=tablepro
ExecStart=/opt/tablepro/tablepro-go --config=/etc/tablepro/connections.json --job=/etc/tablepro/nightly.json
# 作业自己会断开连接；超时兜底防挂死
TimeoutStartSec=30min
```

```ini
# /etc/systemd/system/tablepro-nightly.timer
[Unit]
Description=Run TablePro nightly sync

[Timer]
OnCalendar=*-*-* 02:30:00
Persistent=true

[Install]
WantedBy=timers.target
```

`Persistent=true` 让机器停机错过的运行在开机后补跑一次——这正是「昨晚没跑」最需要的语义。

## Windows 任务计划

```powershell
$action = New-ScheduledTaskAction -Execute 'C:\tools\tablepro-go.exe' `
  -Argument '--config=C:\ProgramData\TablePro\connections.json --job=C:\ProgramData\TablePro\nightly.json'
$trigger = New-ScheduledTaskTrigger -Daily -At 2:30am
# 用「仅当用户登录时运行」以外的账号时，连接文件的口令要能被该账号解开
Register-ScheduledTask -TaskName 'TablePro nightly' -Action $action -Trigger $trigger `
  -User 'DOMAIN\svc-tablepro' -RunLevel Highest
```

任务计划的「上次运行结果」会显示退出码：`0x0` 成功，`0x1` 步骤失败，`0x2` 配方错误。

## 日志

每一步都会往 stderr 写一行（`job "nightly" 步骤 sync items: ok=true ...`），结尾把整份 `JobResult` JSON 打到 stdout，供调度器或包装脚本采集。日志行经过脱敏：`password=` / `token=` / `secret=` 之类的值会替换成 `***`，所以把日志贴进工单不会带出口令。
