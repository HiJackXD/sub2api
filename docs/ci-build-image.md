# sub2api fork：GitHub Actions 只构建镜像，部署机手动升级

CI 只负责把镜像推到 `ghcr.io/<owner 小写>/sub2api`。部署机在内网，升级时在机器上自己执行：

```bash
docker compose pull && docker compose up -d
```

`.github/workflows/` 里只保留 `build-image.yml`。上游的 `release.yml`、`backend-ci.yml`、`security-scan.yml`、`cla.yml` 已从本 fork 删除：`release.yml` 会在 `v*` tag 上往同一个 GHCR 仓库推 `latest` 和版本号，和这条流水线冲突。不要再加 `build-deploy.yml` 或自托管部署 workflow。

## 文件

| 路径 | 作用 |
|---|---|
| `.github/workflows/build-image.yml` | push `main` / `v*` tag / 手动触发 → buildx 构建并推送 GHCR |
| `deploy/docker-compose.yml` | 部署机 compose。镜像是 `${SUB2API_IMAGE}:${SUB2API_TAG}`，卷是本地目录 `./data`、`./postgres_data`、`./redis_data` |
| `deploy/.env.example` | 含 `SUB2API_IMAGE` / `SUB2API_TAG` 占位符，不含真实密钥 |

## 流水线

- 触发：push `main`（忽略纯 markdown / `docs/**`）、push `v*` tag、手动 `workflow_dispatch`。
- 镜像 `ghcr.io/<owner 小写>/sub2api`，用 `GITHUB_TOKEN` 推送（job 有 `packages: write`），不需要额外 secret。
- Tag 规则：
  - `main` → `latest` + `sha-<7位>`
  - `v1.2.3` → `1.2.3`、`1.2`、`1`、`sha-<7位>`（tag 构建不打 `latest`，`latest` 始终等于 main）
- 缓存：buildx `type=gha,mode=max`；Dockerfile 自带 BuildKit cache mount。
- 构建参数：`VERSION`（tag 构建时取 tag 去掉 `v`；main 构建留空，用 `backend/cmd/server/VERSION`）、`COMMIT`、`DATE`。`GOPROXY` / `GOSUMDB` 改回官方源（上游 Dockerfile 默认是 goproxy.cn）。
- 架构：默认 `linux/amd64`。需要 arm64 时在仓库 Variables 设置 `IMAGE_PLATFORMS=linux/amd64,linux/arm64`。

Actions 页面可以看到这次推送的 tag。部署时把 `.env` 里的 `SUB2API_TAG` 改成要上的 tag（建议用不可变的 `sha-<7位>`，也可以用 `latest` 或 `1.2.3`）。

## 部署机：第一次初始化

需要 Docker Engine ≥ 24，以及 Compose v2（`docker compose version` 能输出）。不支持 `docker-compose` v1。执行用户要在 `docker` 组里。

```bash
sudo mkdir -p /opt/sub2api && sudo chown "$USER" /opt/sub2api && cd /opt/sub2api
# 放入仓库里的 deploy/docker-compose.yml 和 deploy/.env.example
cp .env.example .env && chmod 600 .env
sed -i "s|^SUB2API_IMAGE=.*|SUB2API_IMAGE=ghcr.io/<owner小写>/sub2api|" .env
# 第一次可先用 latest；之后改成具体 sha-xxxxxxx 或版本号
sed -i "s|^SUB2API_TAG=.*|SUB2API_TAG=latest|" .env
for k in JWT_SECRET TOTP_ENCRYPTION_KEY POSTGRES_PASSWORD; do
  sed -i "s|^$k=.*|$k=$(openssl rand -hex 32)|" .env
done
mkdir -p data postgres_data redis_data
docker compose pull && docker compose up -d
```

`JWT_SECRET` 和 `TOTP_ENCRYPTION_KEY` 必须固定。留空的话，容器每次重建都会让用户掉登录，已有的 2FA 也会失效。

### 拉取私有镜像

fork 后第一次推送的 GHCR 包默认是 private。

- 把包改成 Public（Packages → sub2api → Package settings → Change visibility）后，部署机不用登录。
- 保持 Private 时，在部署机用只含 `read:packages` 的 classic PAT 登录一次：

```bash
echo <PAT> | docker login ghcr.io -u <github用户名> --password-stdin
```

包需要关联到本仓库（Package settings → Manage Actions access），否则 workflow 的 `GITHUB_TOKEN` 推不上去。

## 升级和回滚

在部署目录里改 `.env` 的 `SUB2API_TAG`，然后拉取并重建：

```bash
cd /opt/sub2api
# 例如改成某次 main 构建：SUB2API_TAG=sha-a1b2c3d
# 或某个版本 tag：SUB2API_TAG=1.2.3
docker compose pull && docker compose up -d
```

`SUB2API_IMAGE` 一般不用改。回滚就是把 `SUB2API_TAG` 改回旧的 `sha-xxxxxxx`（或版本号），再执行同一条命令。

如果以前用的是命名卷 `sub2api_data` / `postgres_data` / `redis_data`，换成这份本地目录卷之前要先把数据迁出来。

## 和其他 workflow 的关系

本 fork 的 `.github/workflows/` 只有 `build-image.yml`。上游的 `release.yml`、`backend-ci.yml`、`security-scan.yml`、`cla.yml` 已删除，不会再跑 CI、安全扫描、CLA 或 GoReleaser 发版。需要那些流程时从上游仓库把对应文件加回来；不要和 `build-image.yml` 同时响应同一个 `v*` tag 去推 `ghcr.io/<owner>/sub2api`。

## 二开注意

程序内置的在线更新写死了上游仓库（`backend/internal/service/update_service.go` 里 `githubRepo = "Wei-Shaw/sub2api"`）。在 fork 里点在线更新，会换成上游 release，二开改动会被覆盖。容器部署的升级走上面的手动 `docker compose`，不要用内置在线更新。
