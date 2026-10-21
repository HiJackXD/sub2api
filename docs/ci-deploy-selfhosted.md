# sub2api fork：GitHub Actions 构建 + docker compose 自动部署方案

> 本 fork 只启用**方案 B**（内网部署机、self-hosted runner）。不要再加入 `build-deploy.yml`（公网 SSH 方案）。上游 `release.yml` 已改名为 `.github/workflows/release.yml.disabled`，避免 `v*` tag 时两边同时往 `ghcr.io/<owner>/sub2api` 推 `latest` / 版本号。

## 文件
| 路径 | 作用 |
|---|---|
| `.github/workflows/build-deploy.yml` | **方案 A**：构建 → GHCR → 通过公网 SSH 部署（appleboy/ssh-action）。**本 fork 未加入此文件** |
| `.github/workflows/build-deploy-selfhosted.yml` | **方案 B**（本 fork 使用）：构建 → GHCR → 部署机上的 self-hosted runner 本地执行 compose。与方案 A 二选一 |
| `deploy/docker-compose.yml` | 部署机用 compose，基于上游 `deploy/docker-compose.yml`：镜像改成 `${SUB2API_IMAGE}:${SUB2API_TAG}`，卷改成本地目录 `./data`、`./postgres_data`、`./redis_data` |
| `deploy/.env.example` | 上游 `.env.example` + 新增 `SUB2API_IMAGE` / `SUB2API_TAG`（占位符，不含真实密钥） |
| `.github/workflows/release.yml.disabled` | 上游 release workflow，已禁用。恢复文件名 `release.yml` 才会重新生效 |

## 流水线行为
- 触发：push `main`（忽略纯 md/docs 改动）、push `v*` tag、手动 `workflow_dispatch`（可选不部署）。
- 镜像 `ghcr.io/<owner 小写>/sub2api`，用 `GITHUB_TOKEN` 推送（job 有 `packages: write`），无需额外 secret。
- Tag 规则：
  - `main` → `latest` + `sha-<7位>`
  - `v1.2.3` → `1.2.3`、`1.2`、`1`、`sha-<7位>`（tag 构建不打 `latest`，`latest` 始终等于 main）
- 缓存：buildx `type=gha,mode=max`；Dockerfile 自带 BuildKit cache mount。
- 构建参数：`VERSION`（tag 构建时取 tag 去掉 v；main 构建留空 → 用 `backend/cmd/server/VERSION`）、`COMMIT`、`DATE`，并把 `GOPROXY/GOSUMDB` 改回官方源（上游默认 goproxy.cn）。
- 架构：默认 `linux/amd64`；有 arm64 机器就设仓库变量 `IMAGE_PLATFORMS=linux/amd64,linux/arm64`（前端/Go 编译在原生架构交叉编译，开销不大）。
- 部署：始终部署不可变的 `sha-xxxxxxx` tag，并**写回部署机 `.env` 的 `SUB2API_TAG`**，之后手动 `docker compose up -d` 也不会漂移；然后 `docker compose pull && docker compose up -d --remove-orphans`，等 `sub2api` 容器 healthy（最多 180s），失败会打印日志和回滚提示。
- 多台部署机：仓库变量 `DEPLOY_TARGETS` 写一个 JSON 数组，每个名字对应一个 **GitHub Environment**，用 matrix 逐台滚动（`max-parallel: 1`，`fail-fast: false`）。Environment 还能加审批人 / 限定分支（例如 `prod` 只允许 tag 部署）。
- 回滚：在部署机上把 `.env` 里的 `SUB2API_TAG` 改成旧的 `sha-xxxxxxx`（或者版本号），然后 `docker compose up -d`。

## 需要配置的东西
### 仓库级 Variables（Settings → Secrets and variables → Actions → Variables）
| 名称 | 示例 | 说明 |
|---|---|---|
| `DEPLOY_TARGETS` | `["prod-1","prod-2"]` | 不设置 = 只构建不部署 |
| `IMAGE_PLATFORMS` | `linux/amd64,linux/arm64` | 可选，默认 amd64 |
| `DEPLOY_SYNC_COMPOSE` | `true` | 可选：每次部署顺带把仓库的 `deploy/docker-compose.yml` 覆盖到部署机（`.env` 不动） |

### 每个 Environment（Settings → Environments → `prod-1` …）的 Secrets（方案 A）
| 名称 | 必填 | 说明 |
|---|---|---|
| `DEPLOY_HOST` | ✅ | 部署机 IP/域名 |
| `DEPLOY_USER` | ✅ | SSH 用户（需要在 `docker` 组里） |
| `DEPLOY_SSH_KEY` | ✅ | 私钥全文（建议给 CI 单独生成 ed25519 密钥，公钥放进部署机 `authorized_keys`） |
| `DEPLOY_PATH` | ✅ | 部署目录，例如 `/opt/sub2api` |
| `DEPLOY_PORT` | ❌ | 默认 22 |
| `DEPLOY_SSH_FINGERPRINT` | ❌ | 主机公钥 SHA256 指纹（`ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub`），防中间人攻击，**建议设置** |
| `GHCR_PULL_USER` / `GHCR_PULL_TOKEN` | ❌ | 部署机长期拉取私有包用的 PAT（见下文）；不设置时用本次 job 的临时 `GITHUB_TOKEN` 拉取，放在临时 `DOCKER_CONFIG` 里，用完删除 |

方案 B 不需要 SSH secrets，只要在 Environment 里设置 **Variable** `DEPLOY_PATH`（默认 `/opt/sub2api`）。

## 部署机前置条件
1. Docker Engine ≥ 24 + Compose v2 插件（`docker compose version` 能正常输出）。不支持老的 `docker-compose` v1。
2. 部署用户在 `docker` 组：`sudo usermod -aG docker <user>`。
3. 初始化目录（只需一次）：
   ```bash
   sudo mkdir -p /opt/sub2api && sudo chown $USER /opt/sub2api && cd /opt/sub2api
   # 放 docker-compose.yml 和 .env.example（scp 或从 fork 的 raw 链接下载）
   cp .env.example .env && chmod 600 .env
   sed -i "s|^SUB2API_IMAGE=.*|SUB2API_IMAGE=ghcr.io/<owner小写>/sub2api|" .env
   for k in JWT_SECRET TOTP_ENCRYPTION_KEY POSTGRES_PASSWORD; do sed -i "s|^$k=.*|$k=$(openssl rand -hex 32)|" .env; done
   mkdir -p data postgres_data redis_data
   ```
   `JWT_SECRET`、`TOTP_ENCRYPTION_KEY` 一定要固定，否则容器每次重建后用户都会被登出，2FA 也会失效。
4. GHCR 登录：
   - **包设为 Public**（GitHub → 你的 Packages → sub2api → Package settings → Change visibility）：部署机无需登录。
   - **包为 Private**（fork 后首次推送默认就是 private）：
     - CI 自动部署：workflow 已经用 job 临时 token 登录，**不用额外配置**（前提：这个包是由本仓库的 workflow 推送的，默认会关联到仓库并继承访问权限；如果没有，到 Package settings → Manage Actions access 里把仓库加上）。
     - 部署机上手动 `docker compose pull`：要用 **classic PAT，只勾 `read:packages`**：`echo <PAT> | docker login ghcr.io -u <github用户名> --password-stdin`。也可以把这个 PAT 填进 `GHCR_PULL_TOKEN`，让 CI 每次用它登录并保留登录状态。
5. 方案 A：部署机 SSH 端口要能从公网访问（GitHub 托管 runner 的 IP 不固定，范围见 `https://api.github.com/meta` 的 `actions` 字段，很大，基本没法靠 IP 白名单收紧）。
6. 方案 B：在每台部署机上安装 runner（Settings → Actions → Runners → New self-hosted runner），注册时加 label `sub2api-<target名>`（例如 `sub2api-prod-1`），用 `./svc.sh install` 装成服务；runner 用户要在 `docker` 组，并且对 `DEPLOY_PATH` 有写权限。runner 只需要出站 HTTPS 能访问 github.com / ghcr.io。注意：**公开仓库不要用 self-hosted runner**（别人的 fork PR 可能在你机器上执行代码）。本 workflow 只响应 push/手动触发，但仍建议到 Settings → Actions → General 里把「Fork pull request workflows」设成需要审批。GitHub 上公开仓库的 fork 不能直接改成 private；如果需要私有，就用 `git clone --bare` + `git push --mirror` 的方式新建一个私有仓库。

## fork 后需要一起处理的上游 workflow
- `release.yml`：同样在 `v*` tag 上触发，会用 GoReleaser 往 `ghcr.io/<owner>/sub2api` 推 `latest`/版本号，还会建 Release、推送 VERSION 文件、发 Telegram 通知，**跟本方案的 tag 冲突**。本 fork 已把它改名为 `.github/workflows/release.yml.disabled`（Actions 只加载 `.yml` / `.yaml`，因此不会运行）。若要恢复上游发版，把文件名改回 `release.yml`，并避免与本 workflow 同时响应同一个 `v*` tag。
- `backend-ci.yml` / `security-scan.yml`：每次 push 都会跑（其中一个 job 用 macos-15），可以保留做 CI；想让 CI 通过后再部署，可以在 build job 前加 `needs` 或改用 `workflow_run`。
- `cla.yml`：带 `github.repository == 'Wei-Shaw/sub2api'` 判断，fork 里不会执行。

## 二开注意事项
- 程序内置的「在线更新」检查写死的是上游仓库（`backend/internal/service/update_service.go` 里 `githubRepo = "Wei-Shaw/sub2api"`）。在 fork 版本里点在线更新，会把二进制换成**上游**的 release，你的二开改动会被覆盖。建议改成自己的仓库，或者在 UI/配置里禁用；容器化部署的话，升级统一走 CI。
- 上游 `deploy/docker-compose.local.yml` 比 `docker-compose.yml` 落后（缺 postgres 调优参数、`ALIPAY_*` 变量，健康检查也更弱），所以本方案以 `docker-compose.yml` 为基础，只把卷改成本地目录。
