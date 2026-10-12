# go-coding — Go 代码智能体（Coding Agent）

> ⛔ **项目已封版（2026-10-12）** —— 这是一个**面试作品**，功能开发到此为止，不再新增特性或维护；仓库保留可运行、可复现的最终状态。封版说明见文末 [项目状态](#项目状态)。

> 对标 Claude Code 的 **Go 代码智能体 CLI**。纯 Go 从零实现（零 AI SDK 依赖）。
> **借鉴 Claude Code 证明有效的通用范式，核心价值全部自研** —— 符号级理解、编辑时符号校验、量化评测闭环，这三件是 Claude Code 没有的。

## 它做什么

给一个 Go 项目 + 一句需求，Agent 自主完成 **需求拆解 → 代码生成/修复 → 单元测试 → 报错分析 → 自动修复**：

```
$ go run ./cmd/agent -repo testdata/cases/fix_add \
    -req "The Add function is broken. Fix it so it returns the correct sum of its two integer arguments."
== result ==
state=done rounds=1 toolCalls=2 note=tests passed
```

它也**暴露成 MCP Server**，所以 VSCode / Claude Desktop / Cursor 都能直接调用它的原子工具（`cmd/mcp-server`）。

## 核心亮点（自研，Claude Code 没有）

| 亮点 | 说明 |
|---|---|
| **符号级上下文引擎** | AST 解析 → 符号索引 + 调用关系图，O(1) 精确命中 + 调用链 BFS。**零向量依赖**。Claude Code 只有 grep + 读文件，没有符号表。**P17 receiver-aware 检索**：方法符号存裸名 + `Recv` 字段 + `byQualified` 二级索引——查询里写 `(*Cache).Get`（与编辑 `recv` 同一渲染）O(1) 精确命中方法，检索与编辑说同一门接收者语言；**P18 类型感知调用解析**：`c.Get()` 按 `c` 的声明类型（`&Cache{}`/`new(Cache)`/`var c Cache`/参数）精确定位方法——调用图从「声明顺序的偶然」变成「声明类型的必然」，编辑 recv / 检索 `(*Cache).Get` / 调用图 `c.Get()` 三级接收者语言闭环；**P19 包级类型表**：`pkgFacts`（函数返回签名 + struct 字段表）按包聚合跨文件，`c := makeCache()` 从返回签名推类型、`p.c.Get()` 顺着字段链推类型——调用图边只由「类型」决定 |
| **编辑时符号校验** | 每次 Go 编辑过三关：语法 → gofmt → **调用图回归**。删了还被跨文件调用的符号 → 毫秒级拒绝 + 回滚 + 精确报调用者。Claude Code 没有调用图，只能等编译器/测试报错（秒级）|
| **量化评测闭环** | mock/live 双模式评测 + 隐藏验收测试。把「Agent 能力/性能」变成可回归的指标（成功率/轮次/延迟/**token 成本**），全部落 JSON |
| **自举用例（dogfooding）** | 评测用例集里有一个是**它修自己**：fixture = 本项目符号引擎的真实源码（4 文件 ~500 行）+ 一个真实 bug。证明「能改真实项目」而不是只改 testdata 玩具 |
| **生命周期 Hooks** | before/after 编辑、测试、失败节点插入回调，返回 error 即 veto（拦截 + 修复），安全策略可插拔 |
| **Token 成本计量** | 解析 LLM `usage` 并累计到每任务，`harness -price-per-1m` 输出估算成本 |
| **AST 级编辑 + 成本闸门（P11/P12/P14/P16）** | 编辑定位手段升级：`ApplySymbol` 按**声明名**定位顶层声明（函数/方法/类型/常量）、`ApplyLocal` 按**「所在函数 + 局部名」**定位函数体内局部变量的**声明点**，splice 字节区间——不用给旧正文，分组/多名字声明、重名、shadowing 一律拒绝，同一校验链复用；**P16 receiver 消歧**：同名方法加 `recv`（接收者渲染与符号引擎同规则）按接收者精确命中、方法与普通函数/类型互不串扰；成本治理补上：新钩子点 `before_chat` + `Event.tokens` 累计值 → `-max-tokens` 超预算**立即终止任务**（唯一能在「花钱」前停住支出的否决点）；**P14 按任务类型分预算**（`harness -max-tokens-fix/-impl/-self`）——评测按用例复杂度分级设护栏（bugfix/impl/self 三档），同一个 before_chat 机制、零新原语 |
| **Evaluator-Optimizer 编排（P6）** | Anthropic 官方模式，plan 级评审：Producer 产出 plan → **符号完整性门**（`go/parser` 预演 plan 的符号效果 vs 基线，会删符号/改包名 → 未编辑即拒，零 token）→ **独立 LLM 评审**（`PASS\|NEEDS_IMPROVEMENT\|FAIL` + feedback），PASS 才编辑，否则 feedback 喂回重写。评估对象是生成的 plan（坏 plan 零落盘、零测试轮）。`harness -arch single\|pe` 对比跑，用 Harness 数据量化「加一个评审角色的代价」 |
| **放弃向量检索的决策** | 代码持续变化 → 语义索引重建成本高；符号精确匹配已覆盖大部分场景；Claude Code 本身也不用向量检索 |
| **Skill + MCP client + SubAgent（三件套）** | 动态工具注册表的三类消费方：Skill 技能包（`skills/<name>/SKILL.md`，模型按描述自主调用）；MCP client（与 server **共享同一份手写 JSON-RPC 协议**，`-mcp` 拉起外部工具，**自举测试**连自己 server）；SubAgent 子任务分发（`agents/<name>.md` 注册，Researcher 只读报告 / Worker 全编辑，**共享父 sandbox + 符号索引**，改的代码父立刻可见，结果 JSON 回传） |
| **多语言测试解耦（P20）** | 框架层语言无关：`-test-cmd "python -m pytest"` 解 `run_test` 的 Go 假设（默认仍 `go test ./...`），`-allow python,pytest` 显式 opt-in 语言工具进沙箱白名单（`AllowCommands` 合并语义，`"*"` 通配覆盖无固定子命令集的测试器）——安全不靠默认放开，能力取决于给不给工具 |

## 架构

```
┌──────────────────────── cmd/agent / cmd/mcp-server / cmd/worker ──┐
│  单调度 Agent 状态机（plan→edit→execute→validate→repair）          │
│  探索优先编码循环 · 会话记忆 · repair-before-test                   │
└───────┬───────────────────────────────────────────────────────────┘
        │ 调用（只读可并行）
┌───────▼───────────────────────────────────────────────────────────┐
│  原子工具集 internal/tools     read·grep·list·edit·test·rollback   │
├───────────┬──────────────┬──────────────┬───────────────┬──────────┤
│ codeindex │ edit         │ sandbox      │ harness       │ queue    │
│ 符号+调用图│ old→new      │ 逻辑隔离沙箱  │ mock/live评测  │ Redis队列│
│ 两级检索   │ +校验链       │ 路径/白名单/  │ +隐藏验收测试   │ WorkerPool│
│           │              │ 快照/回滚     │               │          │
└───────────┴──────────────┴──────────────┴───────────────┴──────────┘
```

| 包 | 职责 |
|---|---|
| `pkg/llm` | 手写 OpenAI 兼容客户端（SSE 流式 / 重试 / 截断检测）|
| `internal/agent` | 状态机 + 循环引擎 + 探索优先 + 会话记忆 |
| `internal/codeindex` | ⭐ AST 符号索引 + 调用图 + 两级检索 + 编辑时符号校验 |
| `internal/edit` | ⭐ 精准编辑：文本级 old→new 唯一匹配 + **AST 级 ApplySymbol（按声明名定位）+ ApplyLocal（按「函数 + 局部名」定位声明点）+ recv（同名方法按接收者精确命中）** + 校验链 |
| `internal/sandbox` | 逻辑隔离沙箱（Windows 路径/白名单/快照/进程树）|
| `internal/harness` | ⭐ 量化评测（mock/live + 隐藏验收测试）|
| `internal/mcpserver` | MCP Server（JSON-RPC 2.0 + 9 工具 + edit 确认流 + 每连接隔离）|
| `internal/mcprpc` | ⭐ 共享 JSON-RPC wire 层（server/client 一个协议两个方向）|
| `internal/mcpclient` | MCP client（stdio/HTTP + 自举测试 + `AsToolDefs` 零翻译注册）|
| `internal/skill` | Skill 技能包（`skills/<name>/SKILL.md` → `skill:<name>` 动态工具）|
| `internal/subagent` | 子代理注册（`agents/<name>.md` → Researcher/Worker，共享状态派发）|
| `internal/frontmatter` | 共享 YAML-ish frontmatter 解析器（skill/subagent 同源）|
| `internal/queue` | 吞吐层（Redis 队列 + WorkerPool + 手写 RESP client）|
| `internal/replay` | 事件日志回放 |
| `internal/agent/hooks.go` | 生命周期 Hooks（before/after 编辑测试 + on_error，veto 语义）|

## 快速开始

环境：Go 1.25+（Windows 下 go 不在 PATH 需 `export PATH="E:/go1.25.12.windows-amd64/go/bin:$PATH"`）。

```bash
# 1. 单任务（mock 或 live）
go run ./cmd/agent -repo testdata/cases/fix_add -req "The Add function is broken. Fix it."
# 1b. 交互式 REPL（连续对话，同一 workspace + 记忆）
go run ./cmd/agent -repo testdata/cases/fix_add -interactive
# live 模式需环境变量 —— 最简单的姿势：复制 .env.example 为 .env 填上
# OPENAI_API_KEY（三个入口启动时自动加载根目录 .env；shell export 优先）：
#   cp .env.example .env && 编辑 .env 填密钥
#   或手动 export OPENAI_BASE_URL=... OPENAI_MODEL=... OPENAI_API_KEY=...
#   scripts/run-live.sh testdata/cases/fix_add "<需求>"

# 2. 全量评测（12 用例）
go run ./cmd/harness -mode mock            # 确定性，CI 安全，3 秒出报告
scripts/gate.sh                            # 门禁自动化：mock 门禁收进 go test（build tag，~3 分钟）
go run ./cmd/harness -mode live -out harness-out/live.json   # 真实 LLM 基线（无外部工具 → 11/12）
scripts/run-live-harness.sh                                  # 完整 12/12：build mcp-demo-server 并 -mcp 挂上（MCP=0 退回 11/12）
go run ./cmd/harness -mode live -mcp "compat ./mcp-demo-server.exe" -price-per-1m 2   # 带外部工具 + 估成本
# P14 按任务类型分预算：bugfix/impl/self 各配一个 token 天花板（0 = 不设闸门）
go run ./cmd/harness -mode live -max-tokens-fix 3000 -max-tokens-impl 5000 -max-tokens-self 15000

# 2a. P6 Evaluator-Optimizer（Anthropic 官方模式，plan 级评审）
go run ./cmd/harness -mode mock -arch pe   # plan 落盘前独立评审：每用例多一次评审调用（avgLLMCalls 1.0→2.0）
go run ./cmd/agent -repo testdata/cases/fix_add -req "Fix the Add function" -arch pe

# 2b. 单任务 + 生命周期钩子 demo（block-paths 演示治理 veto）
go run ./cmd/agent -repo testdata/cases/fix_add -req "Fix the Add function" \
    -block-paths mathops.go

# 3. MCP Server（VSCode/Claude Desktop/Cursor 可用）
go run ./cmd/mcp-server -workdir . -transport http -port 8787   # HTTP（每会话隔离）
scripts/mcp-demo.sh                                              # stdio 单会话演示

# 4. 任务队列（吞吐）
go run ./cmd/worker -mode inline -workers 4 -bench 5 -mock     # 吞吐演示
go run ./cmd/worker -mode redis -workers 2                     # Redis 消费者

# 5. 回放一次运行
go run ./cmd/replay            # 演示时间线
```

## 评测基线（实测数字）

| 模式 | 结果 |
|---|---|
| mock（确定性，CI 门禁） | **12/12 `rate=100% avgRounds=1.0`** |
| mock PE（`-arch pe`，P6 plan 级评审） | **12/12 `rate=100% avgRounds=1.0`**，每用例多一次独立评审调用（`avgLLMCalls=2.0` vs single 1.0） |
| live（gpt-5.6-luna） | **5/5 `rate=100% avgRounds=1.0`**（含模糊需求「并发安全 counter」）|
| live（gpt-5.6-terra 全量 12 用例，无外部工具） | **11/12 `rate=92% avgRounds=1.0 avgLLMCalls=2.0`**（11 个一次通过；唯一 fail `fix_legacy_time`——可见测试故意不锁布局，格式只存在于 MCP 知识工具，无 `-mcp` 时设计上不可解；self_codeindex 首次 live 通过，14828 tokens；`totalTokens=40711`，总耗时 ~9m46s）|
| live（gpt-5.6-terra 全量 12 用例，**带 `-mcp`**） | **12/12 `rate=100% avgRounds=1.0 avgToolCalls=3.1 avgLLMCalls=2.1`**（11 个原有用例无回归；**fix_legacy_time 模型自主 call `mcp:compat:get_time_format`** 补上「九工具没有」的隐藏布局 → toolCalls=5 tokens=2837 通过；`totalTokens=65273`，总耗时 4m5s，`-price-per-1m 2` 估成本 ~$0.13）——两条基线并存：无工具 11/12（设计上不可解的诚实 92%）vs 带工具 12/12，区别只在是否给了外部工具 |
| live SubAgent 冒烟（gpt-5.6-terra） | **researcher 派发成功**（子循环探索→报告回喂→父 done，tokens=23338）+ **worker(coder) 派发成功**（共享 workspace 实现 `Subtract`→test ok，tokens=32832）|
| live MCP client 冒烟（gpt-5.6-terra） | **模型自主 call `mcp:compat:get_time_format`**（`cmd/mcp-demo-server` 领域工具）→ 学到模糊布局 `06_002_150405` → 改对 `FormatTimestamp` → `state=done rounds=1 toolCalls=4 tokens=34021`，隐藏验收通过（P10 单案例冒烟；已在 harness 全量基线 §7#36 升级为 12 用例可复现评测）|
| live（本地 qwen2.5:7b on Ollama） | fix_add 实测 ~1/3 成功率（5-6 轮）；JSON 修复层兜底小模型畸形输出 |
| 吞吐（4 workers） | 6 任务 6/6 passed，`peakConcurrent=4` |
| Redis 跨进程 | 3 任务 → 2 workers → 3/3 passed |
| Token 成本（live） | 每用例 `tokens` 实测 + `-price-per-1m` 估算成本 |
| 成本闸门（P11/P14） | `cmd/agent -max-tokens N` 单任务超预算立即终止；`cmd/harness -max-tokens-fix/-impl/-self N` **按任务类型分级护栏**（bugfix/impl/self 三档，复用同一 before_chat 经济止损点，零新原语）——mock 门禁 12/12 不变（预算 opt-in + mock 不报 usage） |

> 12 个用例：7 bugfix（`fix_add` 加法 / `fix_div` 求平均 / `fix_reverse` 反转 / **`fix_multi` 跨文件调用链** / **`fix_offbyone` 循环边界少算** / **`fix_errcheck` 吞错误** / **`fix_legacy_time` 废弃时间格式迁移**）+ 4 实现（`impl_concurrency` 并发安全 / `impl_parse` 解析器 / **`impl_lru` LRU 缓存** / **`impl_orderedset` 有序集合**）+ **1 自举（`self_codeindex`：修本项目自己的符号引擎，4 个真实源码文件 ~500 行 + 一个真实 bug）**，每个带**隐藏验收测试**（Agent 运行时看不到，防「自写测试测自己」）。
>
> **本地模型**：`OPENAI_BASE_URL=http://localhost:11434/v1 OPENAI_MODEL=qwen2.5:7b` 即可让 Agent 跑本地开源模型（Ollama 的 OpenAI 兼容端点），无需改代码。

## 项目状态

**已封版（2026-10-12），不再维护。**

这是面试作品，目标不是「功能面面俱到」，而是把一套 Claude Code 式的通用框架做到讲得透，再把三点**自研差异化**做扎实、量得出：

- **符号级上下文引擎** —— 符号索引 + 调用图 + 两级检索，**零向量依赖**（Claude Code 只有 grep + 读文件，没有符号表）
- **编辑时符号校验** —— 语法 → gofmt → 调用图回归，删了还被跨文件调用的符号在落盘前被拦下（Claude Code 没有调用图，只能等编译器/测试报错）
- **Harness 量化评测闭环** —— mock/live 双模式 + 隐藏验收测试 + token 成本，把「Agent 能力」变成可回归的指标

框架层能力（交互 REPL、edit、run-test、会话记忆、hooks、subagent、skill、MCP server/client）已对齐 Claude Code，**到此为止不再加深**；符号层的已知边界（外部类型方法、接口分派、类型不确定的调用退名字近似）与多语言增强（每语言符号适配器）**接受现状**。

**封版时的状态**：`go test ./...` 全绿（237 个测试函数，含子测试更多）；mock 门禁 **12/12** `rate=100% avgRounds=1.0`；live 基线已记录（无外部工具 11/12、带 `-mcp` 12/12）。上表「评测基线」即为最终实测数字。

代码仍可正常构建与运行 —— 「快速开始」里的命令全部可用。

## 许可

Copyright © 2026 zlx05 · 保留所有权利
