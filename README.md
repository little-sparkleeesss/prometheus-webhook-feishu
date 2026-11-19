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
- 必须携带 `Authorization: Bearer <token>`。
- 并发上限 10，超出返回 503。
- 成功返回 `{"status":"ok"}`。
- Lark 业务错误、网络错误或超时返回 502，让 Alertmanager 重试。

### `GET /healthz`

校验 `FEISHU_WEBHOOK_URL` 的完整合法性（https、允许的 host、路径前缀、无端口和用户信息），不访问网络。

## 环境变量

| 变量 | 必填 | 说明 |
| --- | --- | --- |
| `FEISHU_WEBHOOK_URL` | 是 | 必须是 `https://open.feishu.cn/open-apis/bot/v2/hook/...` 或 `https://open.larksuite.com/open-apis/bot/v2/hook/...`，不允许端口和用户信息 |
| `WEBHOOK_TOKEN` | 是 | 共享 Bearer token；为空或为字面量 `<no value>` 时启动失败 |
| `PORT` | 否 | 默认 `5000`，监听 `0.0.0.0` |

## 飞书机器人设置

机器人安全设置使用自定义关键词 `Prometheus告警`。卡片标题和 note 包含完整关键词 `Prometheus告警`；正文中的字段名只含「告警」，不含完整关键词。不支持签名校验。

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

## 容器内自检

镜像里的二进制支持 `--self-test`：通过本机 `POST /webhook` 发送一条 `LinkTest` 告警（`cluster=self-test`），完整经过鉴权、payload 解析、卡片构建和 Lark 调用，然后退出。

```bash
kubectl -n prometheus exec deploy/feishu-notify -- /prometheus-webhook-feishu --self-test
```

`deploy/feishu-notify` 按集群里的实际 Deployment 名称替换。

- 成功：退出码 0，输出 `self-test passed: HTTP 200: {"status":"ok"}`，飞书群收到一张 `LinkTest` 卡片。
- 失败：退出码 1，输出 HTTP 状态码和服务端返回的通用错误；具体原因看容器日志里的 `lark_code`、`lark_msg`。

自检只连接容器内的 `127.0.0.1:<PORT>`，不会打印 Webhook URL 和 token。它要求同一容器里的服务正在运行，每次执行都会发一条真实消息。

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

## 镜像

镜像发布到 GHCR：`ghcr.io/little-sparkleeesss/prometheus-webhook-feishu`。

- tag push：`:sha-<short-sha>` 和 `:release-<tag>`。
- 手动 dispatch（`main`）：`:sha-<short-sha>` 和 `:main`。

构建摘要会输出 `image digest: sha256:...`。部署时使用该 digest（`image: ghcr.io/little-sparkleeesss/prometheus-webhook-feishu@sha256:<digest>`），不要长期使用浮动 tag。

## 许可证

本项目基于 [MIT License](LICENSE) 开源。
