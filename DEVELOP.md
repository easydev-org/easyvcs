# DEVELOP — 开发规范

本文件是 easy-vcs org 的开发基准。所有仓库（easyvcs / artifact / agent-gateway /
agent-webui / easysidecar）都应遵循；各仓可在自己的 DEVELOP.md 里补充，但不得放宽本文件的硬性规定。

## 1. 文件长度（硬性）

**一个代码文件尽量不超过 500 行；最多不超过 1000 行。**

- 超过 500 行：应当在同一次改动中**拆分**，或至少说明为何暂不拆（在 MR 描述里）。
- 超过 1000 行：**不得合入**（reviewer 应拒绝），除非在下方「例外」中登记。
- **例外（不计入）**：自动生成的文件（`*_gen.go`、`*.pb.go`、`src/gen/**`、protobuf 生成物、`package-lock.json` 等）与纯数据/清单文件。测试文件同样适用本规则（超长测试也应拆成多个文件）。
- 拆分原则：**按职责拆**（一个文件一个主题），不要按行数机械切；同包内拆成多个文件即可（Go 同包跨文件共享，无需改 import）；跨语言按模块/职责拆目录。
- 新增代码**不要**让任何文件突破 1000 行；触碰已有的超长文件时，优先顺手拆分。

## 2. 例外登记

确因技术原因无法拆分（如必须单文件注册的生成物、协议常量表）的，在 `DEVELOP.md` 的「例外」小节列出路径与理由，并由维护者批准：

| 路径 | 行数 | 理由 |
|---|---|---|
| （示例）`cmd/easyvcs/main.go` | — | 待拆分，见 issue |

## 3. 其它既有约定（简短，避免与 README 重复）

- **提交/分支**：功能走分支 + MR 到 `main`；`main` 只经 MR 变更。
- **格式与检查**：`gofmt`/`go vet`/`go test ./...` 必须全绿；TS 侧跑 `tsc`/`vitest`。
- **依赖**：新增依赖需在 MR 说明理由。
- **密钥**：绝不提交真实凭据；示例值标注 `CHANGE THIS`。
- **文档**：端点/契约变更同步更新 `docs/`（如 `docs/api.md`）。
- **语言**：代码注释用英文；面向人的文档可用中文。

## Packages via artifact (not GitHub / public registries)

本模块（`github.com/easylab-platform/easyvcs`）是**内部 Go module**，不在任何公网 registry 上；
被 agent-gateway 等消费。**发布与消费都走私有 artifact**（权威指南：
`easy-vcs/deploy:PUBLISHING.md`）。

```sh
ARTIFACT=http://artifact.worker.svc.cluster.local   # 即 artifact.<ns>.svc.cluster.local

# 发布（release）：把模块 zip PUT 到 artifact 的 Go proxy（需要 write token）
curl -X PUT --data-binary @easyvcs.zip \
  "$ARTIFACT/artifacts/go/upload?name=github.com/easylab-platform/easyvcs&version=vX.Y.Z"

# 消费：GOPROXY 指向 artifact，GOSUMDB=off
export GOPROXY=$ARTIFACT/artifacts/go GOSUMDB=off
```

要点：

- **pull 匿名**；**push 需 write token**（dev 默认 `dev-artifact-token`；真实部署见 `deploy` 的 `values.yaml` → `secrets.artifactToken`）。
- zip 必须是 **proxy 规范形状**（顶层目录 `<module>@<version>/`，根含 `go.mod`）——参见 platform 仓的 `scripts/publish-module.sh`。
- **只打 tag 不够**：module 必须**发布进 artifact**，下游 `go get` 才能解析（artifact 的 Go proxy 只服务 hosted/缓存过的模块，不读私有 Forgejo）。
- **校验和以 proxy 为准**：下游 `go.sum` 用 artifact proxy 提供的哈希；`GOSUMDB=off`（或 `GONOSUMDB`/`GOPRIVATE`）。

## 4. 当前超长文件（待拆分）

维护者已知的超长文件，按优先级拆分（不要求一次全清，但**新增改动不得加剧**）：

- `cmd/easyvcs/main.go`（>1200）— CLI 子命令应拆到 `cmd/easyvcs/*.go`
- `revision/revision_ops.go`（>1100）— 按操作类别拆分
- `cmd/easyvcs/remote.go`（>900）
- `server/server.go`（>650）、`gitbridge/gitbridge.go`（>580）、`server/rr.go`（>500）

## 5. 合并前判断（review 方法）

判断「一个分支合并后会怎样」，**不要用两点式 tip-to-tip diff**（`base...head` 或
`main..branch`）——当分支的 base 陈旧时，它会**显示为「删除」较新文件**（假象），
据此会误判为「会回退」。

- **正确做法**：用**三方合并/merge-base**判断。例如本地：
  `git merge-base --is-ancestor <head> main`（YES = head 已在 main，合并为 no-op），
  或 `git merge --no-commit --no-ff <branch>` 后看 `git diff --cached`。
- 平台工具的 `repo-diff(base, head)` 是**两点式**，**仅**适合「head 相对 base 新增了什么」；
  判断「合并影响」必须用 merge-base 语义。
- **同理**：一个 MR 的 head 若已是 main 祖先（改动已被别的 MR 合入），它是**重复/no-op**，
  应**关闭**而非合并（合并会报 405「无内容」）。
