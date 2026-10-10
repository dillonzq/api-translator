# 协议转换说明

[English](protocol-conversion.md) | 简体中文 · [返回 README](../README.zh-CN.md)

本文件说明当前实现的映射和限制。三种协议支持请求、普通响应及流式响应的全部六个跨协议方向；格式标识和接入示例见 README。

## 原生路径与职责

同协议请求仅改写 `model`，保留历史 reasoning、compaction、角色、工具及未知字段。模型不变时返回原始字节。库不按模型名称自动清理字段；模型目录、路由和上游兼容规则由调用方负责。

同协议响应通常原样透传。Responses 工具身份恢复，以及流式错误、终止校验和 SSE 包装是例外。原生工具恢复保留 namespace 与未建模的数字字段。

## 请求映射

| 内容 | 当前行为 |
| --- | --- |
| system/developer/instructions | 转成目标协议 system/instructions；Messages 还会合并 user/tool_result 与连续 assistant 内容；历史消息边界不保证原样保留 |
| token 限制 | `max_tokens/max_completion_tokens/max_output_tokens` 映射；Messages 必须有正数限制，缺省或非正数默认 4096；Responses → Chat Completions 的非正数 max_output_tokens 被省略 |
| stop/stop_sequences | Chat Completions ↔ Messages 保留；转 Responses 丢弃 |
| 空内容、内容类型 | 空文本块/无有效内容的消息通常被移除；跨协议未知输入类型、system 图片、无法表示的工具结果会明确报错，不会统一静默删除 |
| tool choice / parallel | 转换 auto/none/required/指定工具；Messages 的并行开关映射为 disable_parallel_tool_use；无法表达的组合报错 |
| 采样参数 | 跨协议映射 `temperature`、`top_p`；未映射的顶层字段（如 `n`、`seed`、penalties、`metadata`、`store`、`service_tier`、`previous_response_id`）会报告已知损耗 |

## 结构化输出

JSON Schema 控制在以下字段之间转换：

| 协议 | 字段 |
| --- | --- |
| Chat Completions | `response_format.json_schema` |
| Responses | `text.format` |
| Messages | `output_config.format` |

Chat 与 Responses 可转换 `json_object`；Messages 需要 `json_schema`，无法表示无 schema 的 `json_object`，会报错。模型是否支持目标 schema 由调用方判断。

Messages 的输出格式不携带 OpenAI 的 schema 名称、描述及可选 strictness：名称和描述的损耗会记录为展示信息，显式 `strict: false` 的映射会记录语义变化。函数工具的 `strict` 在转换中保留，包括显式 `false`。

## 工具身份与搜索

Responses 的 `additional_tools` 合并到工具声明；namespace 展平为函数名称，长名称通过截断和摘要控制长度。所有普通与展平名称统一检查碰撞，响应通过请求状态恢复原名称、namespace 和 custom 身份。

Custom 工具（包括 `apply_patch`）可转换为使用字符串 `input` 的函数工具；缺省 schema 包含必需的 `input`。Responses 输出恢复 custom 调用，涵盖 item、input delta/done 和终止快照。custom grammar 等无法映射的约束会报告损耗。

| 搜索路径 | 支持范围 |
| --- | --- |
| Responses ↔ Messages | 可表示的 web search 声明、查询、位置、允许域名、URL 来源和完成信息 |
| 搜索 → Chat Completions | 无托管搜索执行表示；执行信息的损耗会记录，强制搜索选择无法表示时会报错 |
| 原生请求 | 保留搜索声明和选项，不执行跨协议规范化 |

`tool_search`、`image_generation` 等无法表示的请求工具会报告损耗。namespace 内的托管搜索不作为普通函数展开。Messages 搜索结果的加密内容及不可映射的搜索选项也会报告损耗。

Messages 查询缺少对应搜索结果时，普通与流式转换均将搜索 item 标为 `failed` 并报告缺失结果。失败搜索不发送成功的 `response.web_search_call.completed`；空数组结果可以正常完成。搜索 item 的失败不等于整条响应失败，外层完成状态仍按原协议终止原因转换。

## 推理响应

普通响应与流式响应均覆盖以下六个方向：

| 上游 → 客户端 | 保留方式 |
| --- | --- |
| Chat Completions → Messages | 可读推理映射为 `thinking` 块 / `thinking_delta` |
| Chat Completions → Responses | 可读推理映射为 `reasoning.summary[]`（`type: summary_text`）和对应 summary 流式事件 |
| Messages → Chat Completions | `thinking` 文本合并到 `message.reasoning_content` / `delta.reasoning_content` |
| Messages → Responses | 每个 `thinking` 块映射为独立 reasoning summary item |
| Responses → Chat Completions | reasoning item 的 `summary_text` 与可读 `content`（`reasoning_text` / `text`）合并到 `reasoning_content` |
| Responses → Messages | 同样的可读推理映射为 `thinking` 块 / `thinking_delta` |

Chat Completions 按 `reasoning_content` → `reasoning` → `reasoning_details` 的顺序取第一个包含可读文本的字段，支持字符串、数组和带 `text` 的对象。多个别名不会重复拼接。文本保留空白和 Unicode；不会混进最终回答，也不会生成不存在的推理。Chat Completions 的多个推理片段在 Responses 终止结果中聚合为一个 reasoning item；Messages 的多个 thinking 块分别保留为 reasoning items。Chat Completions 输出本身无法保留正文、推理和多个块之间的原始排列关系。

Responses 的流式转换接收 `response.reasoning_summary_text.delta/done` 与 `response.reasoning_text.delta/done`。`output_item` 和终止响应中的 reasoning 快照补充未通过 delta 发送的文本；相同 part 已发送的前缀不会重复输出。合成 Responses 流包含 item/summary 的开始、增量和结束事件，终止 output 的顺序、索引与 item 公告一致。

跨协议仍不传递 Anthropic `signature` / `signature_delta` / `redacted_thinking.data`，以及 Responses `encrypted_content`。这些是供应商专属的签名或不透明数据，没有可靠的跨协议映射；不会伪造签名，也不会当成可读推理。转换出的无签名 thinking 适合展示，不保证可直接回放到要求有效签名的 Anthropic 请求。

相同协议的响应通常原样透传，保留这些字段；Responses 的自定义工具恢复和流式错误/终止处理是例外。

## 推理控制与后缀

`reasoning_effort` 与 `reasoning.effort` 相互映射。Messages 使用固定 effort/budget 表：

| effort | budget |
| --- | --- |
| `none` | 0，关闭 |
| `auto` | -1，上游默认 |
| `minimal` | 512 |
| `low` | 1024 |
| `medium` | 8192 |
| `high` | 24576 |
| `xhigh` | 32768 |
| `max` | 128000 |

反向转换按阈值选择档位，不保证精确往返；正数 budget 超过 24576 时映射为 `xhigh`，不会自动得到 `max`。库不按模型能力过滤或限幅。

默认入口不解析尾部 `model(value)`。需要后缀优先时，显式使用：

```go
import (
    translator "github.com/dillonzq/api-translator"
    "github.com/dillonzq/api-translator/thinking"
)

suffix := thinking.ParseSuffix(requestedModel)
source := payload
if suffix.Effort != "" {
    source = thinking.StripSupersededReasoning(targetFormat, sourceFormat, source)
}
body, err := translator.ConvertRequest(
    targetFormat, suffix.Model, sourceFormat, source, state,
)
if err == nil {
    body, err = thinking.ApplySuffix(targetFormat, body, suffix)
}
```

识别到的后缀取代请求体中的推理控制；`none` / `0` 关闭，`auto` / `-1` 采用上游默认。档位使用上表，数值后缀在 Messages 中保留精确 budget；Chat 与 Responses 转为 effort。

Messages 禁用时移除 `thinking.display`，后缀覆盖 `output_config.effort`；启用时保留可用的 display。Responses 只覆盖 `reasoning.effort`，保留同级字段。

未知后缀仍可被解析并剥离，但不改变请求体推理参数。`StripSupersededReasoning` 只应在后缀已识别时调用。真实包含括号的模型 ID 是否按字面名称路由，由调用方决定。

历史推理与推理控制不同：Messages → Chat/Responses 丢弃历史 thinking/redacted_thinking；Responses → Chat 丢弃 reasoning items；Chat → Messages/Responses 不映射历史推理字段。Responses → Messages 可保留 summary 为无签名 thinking。

## 响应字段与 usage

| 内容 | 当前行为 |
| --- | --- |
| 文本、函数工具调用 | 转换并保留文本和参数；缺省参数补 `{}`；转 Messages 时解析参数，非法 JSON 报错 |
| 多候选 `choices` | 跨协议只处理第一个候选；同协议透传保留全部 |
| 多个消息 / 文本块 | Chat Completions 聚合正文；合成 Responses 聚合为一个 assistant message，定位在首次文本位置 |
| Chat Completions `message.refusal`、Responses message 内的 `refusal` part | 普通/流式响应保留。Chat Completions ↔ Responses 保留独立 refusal 字段/part；转 Messages 映射为可显示的 text。正常完成的拒绝映射为 content_filter/refusal，工具和截断信号仍优先 |
| 结束原因 | `stop` ↔ `end_turn`、`length` ↔ `max_tokens`、`tool_calls` ↔ `tool_use`、`content_filter` ↔ `refusal`。未知原因回退到普通结束；`stop_sequence` 的具体值不复制 |
| 工具调用与截断并存 | 转 Chat Completions / Messages 时实际工具调用优先于截断原因；转 Responses 时仍以 `length/max_tokens` 映射 `incomplete`，工具信号放在 output items 中 |
| 基础 usage | 输入/输出 token 映射；Chat Completions/Responses 的 total 按输入+输出重算；缺省 usage 合成零值 |
| 缓存 usage | Messages 的 input 不含缓存，转 Chat Completions/Responses 时加上 cache read+creation；反向转 Messages 时扣掉可表示的缓存量并限制为非负。cache read 保留；cache creation 在三个协议间映射为 cache_creation_input_tokens / cache_write_tokens，缺省细项保持缺省，显式零值保留，不额外重复计入输入总量 |
| 推理 token usage | Chat Completions ↔ Responses 保留 `reasoning_tokens`；转 Messages 没有独立细项，仍计入输出总量 |
| 身份与时间 | 响应 id/model 保留；合成 message/reasoning item 的 id 由响应 id 派生。普通跨协议 Chat Completions 的 created 使用当前时间；Responses 流转 Chat Completions 可使用上游 created_at；其他未建模身份/时间字段不复制 |

Responses 的 `failed`、`cancelled` 或协议内 error 返回分类错误；普通跨协议响应的其他非终止状态也会报错。`incomplete` 映射为截断。合成 Responses 仅表达 completed/incomplete，不保留全部响应配置。

## 流式生命周期

公开入口接收任意网络分块，解析完整 SSE 帧，支持多 `data:` 行和 LF/CRLF/CR。输出为完整目标事件；Chat 包含一次 `[DONE]`，合成 Responses 事件带递增 `sequence_number`，原生序号保留。

工具身份完整后才公告；参数先到时缓存。转 Messages 时，未完成的工具会阻塞后续内容，按目标块的开始、增量、结束顺序输出，关闭后不再发送增量。工具 arguments 的 done、output item 和终止快照补齐未收到的尾部。

正文、refusal 和推理的 done/item/终止快照也可补齐遗漏文本；已发送前缀不重复输出。正文按 item、content index 和 part 类型跟踪。

`Finish` 校验有效终止状态。Chat 的 finish_reason / `[DONE]`、Messages 的 stop_reason / message_stop 可以建立结束状态；Responses 必须有有效终止事件。缺失终止信号或残留的无效载荷会报错。错误状态不可恢复，后续调用不生成成功终止。

转换会重建目标事件，SSE 的 comment、retry、id、ping 等信息不保证跨协议保留。

## URL 引用

支持 Chat `annotations`、Responses annotation 事件和 Messages `citations` 的 URL 引用转换，并保留可表示的来源、标题和文本范围。

Chat 的引用偏移只计入正文，不包含独立 refusal；Messages 将 refusal 渲染为可见文本。可表示的 Unicode 文本范围随内容聚合调整，交错内容无法保留的偏移会报告损耗。

转 Messages 时，提前收到的引用等待其引用范围实际输出完整后发送；分片不足时继续等待，不提前丢弃或计入去重。引用不会被推迟到后续排队块之后。真正晚到且对应块已关闭的引用会记录 `closed_block` 损耗，不重开旧块或挂到后文。

页码、文档位置等引用类型以及加密引用字段尚无完整映射，已知损耗会记录。

## 损耗与能力边界

`RequestState.Diagnostics()` 报告已知字段损耗。allow/safe/strict 的配置与行为见 README；safe/strict 只拒绝请求阶段的损耗。规则检查明确的协议字段，不递归分析工具参数、结果或 JSON Schema 中的业务数据。

请求可转换支持的图片 URL 和 base64 来源；system 图片及无法表示的工具结果会报错。响应中 Chat → Messages 可转换支持的图片块，Chat → Responses 的非文本输出及未支持的 Messages 内容块会报错。图像生成结果、音频和更多多模态输出尚不支持。未知 Responses item/part 用原始 JSON 检查实际载荷，包含 `result` / `action` 等未建模内容且无法表示时会报错，不静默生成成功的空结果。

logprobs、供应商扩展、历史 compaction 和签名不保证跨协议保留。原生保留字段也不意味着任何上游模型都接受这些内容；调用方负责能力验证。

## 实现与验证

根包提供公开入口、请求策略和传输边界；各协议 adapter 的 `request.go`、`convert.go`、`stream.go` 负责转换。工具规范化、schema、引用、搜索和诊断的共享逻辑位于 `internal/adapter/shared`，推理控制与后缀位于 `thinking`。

测试覆盖 27 份请求/响应/流式快照、交错工具、晚到身份、引用分片、搜索失败及终止一致性。fuzz 验证网络分块一致性和失败状态不可恢复；这些测试不替代真实上游的兼容验证。命令见 README。
