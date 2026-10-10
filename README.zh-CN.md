# api-translator

<div align="center">

<img src="assets/logo.svg" alt="api-translator Logo" width="144" height="144">

**三种 API 协议，一个 Go 转换库。**

[English](README.md) · **简体中文**

[安装](#安装) · [快速开始](#快速开始) · [流式响应](#流式响应) · [协议文档](docs/protocol-conversion.zh-CN.md) · [版本发布](https://github.com/dillonzq/api-translator/releases)

[![许可证：MIT](https://img.shields.io/badge/license-MIT-64748b?style=flat-square)](LICENSE)
[![版本：v0.1.0](https://img.shields.io/badge/release-v0.1.0-6366f1?style=flat-square)](https://github.com/dillonzq/api-translator/releases/tag/v0.1.0)
[![Go：1.26.7+](https://img.shields.io/badge/Go-1.26.7%2B-0891b2?style=flat-square&logo=go&logoColor=white)](go.mod)
[![依赖：标准库](https://img.shields.io/badge/dependencies-standard_library-0d9488?style=flat-square)](go.mod)

</div>

---

独立的 Go 协议转换库，支持 OpenAI Chat Completions、OpenAI Responses 和 Anthropic Messages 之间的请求、普通响应及 SSE 转换。仅依赖 Go 标准库，要求 Go **1.26.7+**。

Go module：`github.com/dillonzq/api-translator`，包名：`translator`。

## 支持范围

三种协议支持全部六个跨协议方向，也支持同协议请求的模型改写及响应透传。转换覆盖文本、函数工具、推理、usage、结构化输出和可表示的 URL 引用；工具身份恢复、托管搜索和流式限制见[协议转换说明](docs/protocol-conversion.zh-CN.md)。

| 常量 | 格式值 | 协议 |
| --- | --- | --- |
| `FormatChatCompletions` | `chat-completions` | Chat Completions |
| `FormatResponses` | `responses` | Responses |
| `FormatMessages` | `messages` | Messages |

使用方将外部格式标识映射到本库常量。当前仅接受以上三种格式值，不支持 Gemini 或独立的 Codex 格式。

## 安装

```sh
go get github.com/dillonzq/api-translator@v0.1.0
```

## 快速开始

以下例子把 Responses 请求转换为 Messages 请求：

```go
package main

import (
    "fmt"
    "log"

    translator "github.com/dillonzq/api-translator"
)

func main() {
    state := translator.NewRequestState()
    payload := []byte(`{"model":"client-model","input":"Hello"}`)
    body, err := translator.ConvertRequest(
        translator.FormatMessages, "upstream-model",
        translator.FormatResponses, payload, state,
    )
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println(string(body))
}
```

调用方通过自己的 HTTP 客户端发送转换后的请求。收到上游响应后，使用同一份 `state`：

```go
outputBody, err := translator.ConvertResponse(
    upstreamFormat, outputFormat, upstreamStatus, responseBody, state,
)
```

`sourceFormat` 是输入请求的协议，`upstreamFormat` 是上游响应的协议，`outputFormat` 是客户端需要的响应协议；输出协议可以与请求输入协议不同。上游模型名由调用方选择并传入。

每次请求使用独立的 `RequestState`。其零值可用；Responses 请求转换到其他协议时必须传入状态，用于恢复工具名称、namespace 和 custom 身份。其他无映射、无诊断需求的场景允许 `nil`。不要跨请求复用或并发修改状态，同一转换器也应顺序调用。

## 流式响应

```go
stream, err := translator.NewStreamConverter(upstreamFormat, outputFormat, state)
// 检查 err 后再使用 stream。
events, done, err := stream.Feed(networkChunk)
// 按顺序发送 events，同时处理 done 和 err。
tail, err := stream.Finish()
// 上游 EOF 时调用 Finish，发送 tail 并处理 err。
```

`Feed` 接收任意大小的网络字节块，返回可直接写入 SSE 连接的完整事件。支持多 `data:` 行及 LF、CRLF、CR 分隔。Chat 输出包含一次 `data: [DONE]`；合成的 Responses 事件包含递增的 `sequence_number`。

`Finish` 校验 EOF 前是否收到有效终止信号。转换失败后，后续 `Feed` / `Finish` 会继续返回错误，不补发成功结束。一次调用可能同时返回已完成的事件和错误；调用方应保留事件顺序，并停止后续正常转换。

HTTP、鉴权、网络读取、取消、重试及模型能力判断由调用方负责。

## 损耗诊断与错误

设置请求策略，并在转换后读取诊断：

```go
state := translator.NewRequestState()
state.Options.LossPolicy = translator.LossPolicySafe
// 使用 state 执行请求及响应转换，再读取：
diagnostics := state.Diagnostics()
```

| 策略 | 请求行为 |
| --- | --- |
| `LossPolicyAllow`（默认） | 允许已知损耗并记录诊断 |
| `LossPolicySafe` | 拒绝影响请求语义的已知损耗 |
| `LossPolicyStrict` | 拒绝所有已报告的请求损耗 |

响应及流式响应报告已知损耗，不因上述策略单独拒绝；协议错误或无法表示的实际输出仍会失败。诊断包含 `Code`、`Path`、`Message`、`Semantic`、`From`、`To`，返回独立副本。`nil` 状态不收集诊断。规则检查明确的协议字段路径，不递归检查工具参数、结果或 JSON Schema 中的业务数据；严格策略也不代表可以保证所有协议字段无损。

转换错误为 `*translator.Error`，包含 `Class`、`Message`、`StatusCode` 和 `Retryable`。错误构造函数会脱敏；`StatusCode == 0` 表示没有显式 HTTP 状态。调用方决定错误响应和重试策略，包装自己的错误消息时可使用 `translator.Redact`。

网络错误可用 `FromNetwork` 和 `PostFirstByteNetwork` 分类，分别对应收到上游首字节之前与之后。

## 转换边界

同协议请求仅改写 `model`，其他字段保留；模型不变时返回原始字节。跨协议使用直接转换路径，字段保留程度取决于目标协议。

默认入口不解析模型名后缀。需要 `model(high)` 或 `model(8192)` 等约定时，由调用方显式使用 `thinking` 子包；用法和映射见[推理控制与后缀](docs/protocol-conversion.zh-CN.md#推理控制与后缀)。推理档位不会按模型能力过滤，budget 不会自动限幅。

结构化输出、工具、引用、搜索、终止规则及剩余限制统一记录在[协议转换说明](docs/protocol-conversion.zh-CN.md)。

## 开发与验证

```sh
go test -race ./...
go vet ./...
gofmt -l .
```

普通测试包含 27 份转换快照。只有确认协议输出变化符合预期后才更新：

```sh
go test . -run TestProtocolGolden -update
```

流式 fuzz 测试分别验证分块一致性和失败状态不可恢复：

```sh
go test . -run '^$' -fuzz '^FuzzStreamChunkBoundaries$' -fuzztime=10s
go test . -run '^$' -fuzz '^FuzzSSEFailureIsTerminal$' -fuzztime=10s
```

本库采用 [MIT 许可证](LICENSE)。
