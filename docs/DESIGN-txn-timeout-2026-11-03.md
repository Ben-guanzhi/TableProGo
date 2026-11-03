# 事务超时治理设计（TableProGo / `internal/txn`）

- 日期：2026-11-03
- 状态：**设计稿，待评审**。本稿不含行为变更。
- 关联：`POLISH-PLAN-2026-11-02.md` P1-6；`WORKSPACE.md` §40（wave 32 事务悬挂接线）。
- 参考实现：`D:\tools\db\db-dev\internal\db\tx.go`（`DefaultTxTimeout = 10m`、`TxWarnAhead = 1m`、`MaxActiveTx = 8`）；`fynequery/db` 的 manual transaction 路径。

---

## 1. 问题

交互式事务（TablePlus 式 `Begin Transaction`）一旦开启，就没有任何时间维度上的治理。
wave 32 只修了"**关标签 → 回滚**"这一条路径（`TableProGo/txn.go` 的 `forgetSession`），
漏掉的是：**用户把标签留在原处、人走开了**（或 App 被强杀、机器挂起）。此时：

- 事务在服务端**无限期**持有资源：专用池连接被检出、行/表锁、MVCC 快照（PG 的
  xid、MySQL 的 undo log）阻塞 VACUUM/清理；SQLite 下文件锁不释放。
- 客户端侧 `txn.Session` 停在 `Active`，`Describe()` 一直显示"事务中"，但没有任何
  机制让它收敛。

也就是说：**悬挂的存活时间没有上界**。P1-6 要补的就是这个上界。

---

## 2. 三个候选机制

| 候选 | 触发条件 | 收敛动作 | 优点 | 代价 |
|---|---|---|---|---|
| **A. 空闲上限**（idle） | 距上次执行语句超过 N 秒 | 自动回滚 | 不误杀活跃的长批处理；贴合"走开了"的失效模式 | 长事务只要一直在跑语句就永不过期；**倒计时不稳定**（每条语句重置）；需要在语句路径埋活动钩子；"忙碌但忘了"的事务仍无上界 |
| **B. 绝对时限自动回滚**（absolute） | 距 `BEGIN` 超过 N 秒 | 自动回滚 | **最坏情况有上界**；倒计时稳定可预期；实现简单；与参考实现一致 | 误杀合法的长事务（用户需在倒计时内提交或显式延长） |
| **C. 仅 UI 倒计时** | 无（只显示） | 不自动回滚，用户手动提交/回滚 | 最不意外，不会自动丢数据 | **不降低服务端风险**：用户走开时事务照样挂着；需要后端状态端点 + 前端倒计时 UI |

> 说明：C 是"显示"，B 是"策略"。B 配上倒计时显示就是"策略 B + 呈现 C"，二者不是互斥的。

---

## 3. 推荐：B（绝对时限 + 提前预警 + 自动回滚），倒计时作为其呈现

**结论：选 B，用 C 的方式呈现剩余时间。** 理由：

1. **上界优先。** P1-6 要治的是"悬挂无治理"，即最坏情况无界。绝对时限保证任何事务
   无论是否活跃，都在 `BEGIN + TxTimeout` 后被收敛；空闲上限在"事务一直在跑语句但
   人已不在"时永不触发，恰恰漏掉最难缠的那类悬挂。
2. **倒计时稳定。** 绝对时限的剩余时间单调递减，UI 可以显示一条稳定收敛到 0 的倒计时；
   空闲上限的剩余时间每条语句跳回满值，用户看到的倒计时永远不会到 0，也无法判断
   危险。
3. **与参考实现一致。** `db-dev` 的手动事务就是"10 分钟超时 + 提前 1 分钟告警 +
   超时自动回滚"，且 `fynequery` 的 manual transaction 走同一套 `db.Transactional`
   抽象。TableProGo 对齐后，三处行为一致、可互相对照测试。
4. **合法长事务的出口明确。** 绝对时限不靠"自动续期"兜底，而是给用户三个显式出口：
   在倒计时内 `COMMIT`；到点被回滚后重新 `BEGIN`；或（后续阶段）在倒计时 UI 上点
   "延长"。这比"看起来一直在跑、其实已经没人管"更安全。

**明确拒绝 A（空闲上限）作为 v1 默认。** 若团队更看重"绝不误杀长批处理"，A 可作为
`TxTimeout` 的后续 refinement，但需要先解决倒计时不稳定与活动钩子埋点两处成本。

---

## 4. 目标行为（验收口径）

- `BEGIN` 时设定 `deadline = now + TxTimeout`（默认 `10m`，可配置）。
- **执行语句不延长 `deadline`**（绝对时限；这是与 A 的核心分界，PoC 已钉住）。
- 进入 `deadline - TxWarnAhead`（默认 `1m`）窗口后，UI 显示告警与倒计时。
- `deadline` 之后，`Sweep` 自动调用 `Rollback` **恰好一次**，会话回到 `None`，
  并记录 `note = "事务已超时并自动回滚"`。
- 重复 `Sweep`、或 `Commit` 之后 `Sweep`，都不得二次回滚。
- 并发活跃事务数上限 `MaxActiveTx`（默认 `8`），达到上限时 `BEGIN` 拒绝。
- 配置项（复用现有 `AppSettings` 的 JSON 结构）：`txTimeout`（`time.Duration`
  的秒数或 `"10m"` 字符串）、`txWarnAhead`、`maxActiveTx`。

---

## 5. `internal/txn` 提议 API（评审后再落地）

`Session` 新增字段（零值仍为 autocommit，向后兼容）：

```go
type Session struct {
    mu sync.Mutex
    Status Status
    ID     string
    ConnID string
    Database string
    Statements int
    Err string

    // --- P1-6 新增 ---
    deadline  time.Time   // BEGIN 时 now + timeout；零值 = 无时限（默认关闭）
    warnAhead time.Duration
    note      string      // "事务已超时并自动回滚" 之类
}
```

提议方法（签名在评审中可改）：

```go
// Begin 的 opener 契约不变；超时配置经 Options 传入，或作为 Session 字段在 Begin 前设置。
func (s *Session) Deadline() time.Time
func (s *Session) Remaining(now time.Time) time.Duration   // 负值 = 已超时
func (s *Session) Warned(now time.Time) bool               // 进入预警窗口
func (s *Session) Note() string
// Sweep 在 deadline 之后回滚一次；返回是否发生了回滚。内部持锁调用 opener.Rollback。
func (s *Session) Sweep(open Opener, now time.Time) (bool, error)
```

要点：

- `Sweep` 的**检查与回滚必须在 `s.mu` 内成对完成**，与现有 `Begin` 的写法一致：否则
  两个并发 `Sweep` 都看到"未超时/未回滚"，会双重复调 `Rollback`。现有 `Commit`/
  `Rollback` 已经是"先 `resetLocked` 再开锁调用"的写法，`Sweep` 沿用同一纪律。
- `Record(err)` **不得**触碰 `deadline`（绝对时限）。这一点要在实现里写进注释，并由
  PoC 与后续单测钉住。
- `Describe()` 可追加倒计时后缀（如 `事务中 · 3 条语句 · app · 还剩 42s`）。
- `App` 侧需要一个**定时器**（`time.Ticker`，例如每 10s）对所有 `txSessions` 调
  `Sweep`；或前端轮询 `/api/tx-status` 时顺带触发。定时器生命周期与 App 一致。

---

## 6. 接线与分阶段（评审通过后再动）

- **阶段 1（配置 + 状态）**：`AppSettings` 加 `txTimeout`/`txWarnAhead`/`maxActiveTx`；
  `Session` 加字段与只读方法；`/api/tx-status` 返回 `remaining`/`warned`/`note`。
  此时**无自动回滚**，行为不变，只暴露数据。
- **阶段 2（自动回滚）**：App 起一个 sweeper，对 `Active`/`Failed` 会话调 `Sweep`；
  `ForgetTxSession`/`closeTxns` 的既有回滚路径不变。
- **阶段 3（UI 倒计时）**：前端事务状态条显示剩余时间与告警样式；加"延长"按钮
  （若评审要求）。

---

## 7. 待评审问题

1. 默认 `TxTimeout` 取 `10m`（对齐 db-dev）还是更宽松（如 `30m`）？交互式事务多为
   小批量，`10m` 是否够用？
2. 是否提供"延长"？若提供，延长是"重置 deadline"（退化成 A 的变体）还是"一次性加
   `TxTimeout`"？建议一次性加固定量，保持倒计时单调。
3. `Failed`（语句已中止）状态是否也纳入超时 sweep？建议纳入：中止事务同样占资源，
   且只有回滚能收敛。
4. 不支持事务的引擎（`Degraded`）在 `db-dev` 里走"非原子降级执行"。TableProGo 当前
   对不支持 `SQLTxDriver` 的连接是直接报错（`txn.go` 的 `txManagerFor`），是否需要
   同样的降级？建议**不在本期做**，先只对能开事务的引擎上治理。
5. sweeper 的 tick 间隔与 App 退出时的清理（`CloseAllTx` 等价物）放在 App 还是
   `internal/txn`？

---

## 8. PoC

`internal/txn/timeout_poc_test.go` 用测试内的原型模型钉住上述策略：

- 预警窗口前 `warned=false`，窗口内 `true`；
- `Sweep` 在 deadline 后回滚**恰好一次**，重复 sweep 不再回滚；
- `Commit` 之后 `Sweep` 不回滚；
- 执行语句**不延长** deadline（绝对时限）。

原型刻意放在测试里，`internal/txn` 的生产代码本轮**不动**，待本设计评审通过后按
§5/§6 落地。
