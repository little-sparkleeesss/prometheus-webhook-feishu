# prometheus-webhook-feishu

Alertmanager 到 Lark/飞书自定义机器人的单向通知适配器。只用 Go 标准库，编译为静态二进制，运行镜像基于 `scratch`。

链路：

```
Alertmanager --POST /webhook--> prometheus-webhook-feishu --POST--> Lark/飞书自定义机器人
```

同时支持飞书国内版 `open.feishu.cn` 和 Lark 国际版 `open.larksuite.com`。

## 接口

### `POST /webhook`

接收 Alertmanager v4 webhook payload，构造飞书 v1 交互卡片并转发。

- 只接受 `POST`，其他方法返回 405。
- 必须携带 `Content-Length`，body 上限 1 MiB。
- 设置 `WEBHOOK_TOKEN` 时必须携带 `Authorization: Bearer <token>`。
- 并发上限 10，超出返回 503。
- 成功返回 `{"status":"ok"}`。
- Lark 业务错误、网络错误或超时返回 502，让 Alertmanager 重试。

### `GET /healthz`

只检查 `FEISHU_WEBHOOK_URL` 已设置且 host 合法，不访问网络。

## 环境变量

| 变量 | 必填 | 说明 |
| --- | --- | --- |
| `FEISHU_WEBHOOK_URL` | 是 | 必须是 `https://open.feishu.cn/open-apis/bot/v2/hook/...` 或 `https://open.larksuite.com/open-apis/bot/v2/hook/...`，不允许端口和用户信息 |
| `WEBHOOK_TOKEN` | 生产必填 | 共享 Bearer token；为空时启动日志会警告并放行所有请求 |
| `PORT` | 否 | 默认 `5000`，监听 `0.0.0.0` |

## 飞书机器人设置

机器人安全设置使用自定义关键词 `Prometheus告警`。卡片标题、正文和 note 均包含该关键词，不支持签名校验。

## 状态码

| 情况 | 状态码 |
| --- | --- |
| 成功 | 200 |
| JSON 非法、字段缺失 | 400 |
| 方法不对 | 405 |
| token 缺失或错误 | 401 |
| body 超过 1 MiB | 413 |
| 并发超过 10 | 503 |
| Lark 业务失败、网络错误、超时 | 502 |

## 本地测试

不需要在宿主机安装 Go，使用 rootless podman：

```bash
podman run --rm -v "$PWD":/src:Z -w /src docker.io/library/golang:1.27.3-alpine \
  sh -c 'go vet ./... && go test ./...'
```

构建镜像：

```bash
podman build -t prometheus-webhook-feishu .
```

运行：

```bash
podman run --rm -p 5000:5000 \
  -e FEISHU_WEBHOOK_URL='https://open.feishu.cn/open-apis/bot/v2/hook/xxx' \
  -e WEBHOOK_TOKEN='change-me' \
  prometheus-webhook-feishu
```
