# cj2api-go

将 [ChatJimmy](https://chatjimmy.ai) 转换为 OpenAI 兼容 API 的 Go 实现，单文件部署，无需 API Key。

由 TypeScript 原版（参考项目：[qingchencloud/cj2api](https://github.com/qingchencloud/cj2api)）移植而来：接口与行为保持一致，去掉了内置测试页，纯标准库、零第三方依赖。

## 特性

- **OpenAI 兼容** — 标准 Chat Completions API，支持流式（SSE）与非流式响应
- **零依赖单文件** — 纯 Go 标准库，一个二进制即跑，`CGO_ENABLED=0` 静态编译
- **高并发低内存** — 复用全局连接池，上游响应限额读取，流式逐块 Flush
- **开箱即用** — `latest` 与 `vX.X.X` 双 tag 自动构建发布（Linux / Windows / macOS × amd64 / arm64）

## 快速开始

### 方式一：下载预编译二进制（推荐）

从 [Releases](../../releases) 下载对应平台的包，`latest` 永远指向最新稳定版：

```bash
chmod +x cj2api-go-linux-amd64
./cj2api-go-linux-amd64
# 默认监听 http://localhost:8787
```

### 方式二：源码构建

```bash
go build -trimpath -ldflags="-s -w" -o cj2api-go .
PORT=8787 ./cj2api-go
```

## 配置

全走环境变量：

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `PORT` | `8787` | 监听端口 |
| `UPSTREAM_URL` | `https://chatjimmy.ai/api/chat` | 上游 ChatJimmy 地址 |
| `TIMEOUT` | `120` | 上游请求超时（秒） |
| `MODEL_OVERRIDE` | 空 | 非空时服务端只允许该模型，其余 model 均 400 |

## API 接口

### POST `/v1/chat/completions`

标准 OpenAI Chat Completions 接口（`/chat/completions` 为兼容别名），请求与响应格式完全兼容 OpenAI。`stream: true` 时返回 SSE（以 `data: [DONE]` 结尾）；默认模型 `llama3.1-8B`，`top_k`（兼容 `topK`）默认 `8`。

### GET `/v1/models`

标准 OpenAI 模型列表接口（`/models` 为兼容别名）。

## 工作原理

```
客户端 (OpenAI SDK / curl / 任意 HTTP)
  │  POST /v1/chat/completions（标准 OpenAI 请求格式）
  ▼
┌──────────────────────┐
│  cj2api-go（单进程）  │
│  1. 解析请求体        │
│  2. 提取 system 消息  │
│  3. 转换为上游格式    │
│  4. 转发到 ChatJimmy  │
│  5. 解析响应 + stats  │
│  6. 封装为 OpenAI 格式 │
└──────────────────────┘
  │  ChatJimmy 私有协议
  ▼
┌──────────────────────┐
│  chatjimmy.ai/api/chat│
│  返回纯文本 + stats 块 │
└──────────────────────┘
```

## 免责声明

本项目仅供**学习研究和技术测试**使用，请勿用于任何商业用途。作者不对因使用本项目产生的任何损失承担责任。

## License

[MIT](LICENSE)
