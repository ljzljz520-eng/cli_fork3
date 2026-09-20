# 组件能力契约 + 约束求解 + 临时环境契约测试 实施计划

## Repository Research

### 当前行为

`cgapp create`（[cmd/create.go](file:///Users/goodman/swe-project/cli_fork3/cmd/create.go)）通过问卷独立选择 backend（`net/http`/`fiber`/`chi`，映射到 `github.com/create-go-app/{name}-go-template` git 仓库）、frontend（21 个 vite/next/nuxt/sveltekit 选项）、proxy（`none`/`traefik`/`traefik-acme-dns`/`nginx`），随后：

1. clone backend 仓库到 `./backend`；用 `npx/npm` 在 `./frontend` 脚手架前端；
2. 内嵌渲染 [hosts.ini.tmpl](file:///Users/goodman/swe-project/cli_fork3/pkg/registry/templates/hosts.ini.tmpl) 与 [playbook.yml.tmpl](file:///Users/goodman/swe-project/cli_fork3/pkg/registry/templates/playbook.yml.tmpl)；
3. 拷贝内嵌 Ansible roles（docker、backend、redis、postgres、traefik|nginx）与 misc Makefile；
4. `cgapp deploy` 仅执行 `ansible-playbook`（[cmd/deploy.go](file:///Users/goodman/swe-project/cli_fork3/cmd/deploy.go)）。

### 失配根源（本计划要消除的）

- **数据库/缓存不可选**：[playbook.yml.tmpl](file:///Users/goodman/swe-project/cli_fork3/pkg/registry/templates/playbook.yml.tmpl#L15-L20) 无条件部署 postgres + redis，问卷中不存在这两个角色。
- **连接参数硬编码、靠注释人工对齐**：`hosts.ini` 中 `backend_port=5000`、`postgres_port=5432`、迁移路径等全部写死，仅以 "MUST BE MATCH to .env" 提醒；backend `.env` 由外部模板仓库提供，CLI 从不读取或校验。
- **隐式耦合散落各处且无校验**：[backend role](file:///Users/goodman/swe-project/cli_fork3/pkg/registry/roles/backend/tasks/main.yml#L38-L67) 硬编码 Traefik labels；[nginx 模板](file:///Users/goodman/swe-project/cli_fork3/pkg/registry/roles/nginx/templates/default-http.conf.j2#L24-L32) 硬编码 `proxy_pass http://cgapp-backend:{backend_port}/`，无 API base path / CORS 概念；[postgres role](file:///Users/goodman/swe-project/cli_fork3/pkg/registry/roles/postgres/tasks/main.yml#L39-L56) 硬编码迁移挂载路径 `backend/platform/migrations` 与 `migrate/migrate` 镜像；无任何 healthcheck 定义。
- **失败后置**：端口冲突、DSN/env 名不一致、迁移路径缺失、运行时版本不符、health 路由不存在，都要到部署后才暴露。

### 已确认的设计决策

1. 契约来源：**随 CLI 内嵌 catalog + clone 后从模板根目录 `cgapp.contract.yaml` 自动发现**（自定义模板缺契约时降级为 loose 模式，仅告警）。
2. 求解：**纯 Go CSP 回溯 + semver 区间约束**，不引入 SAT/SMT 重依赖。
3. 临时验证：**本地 Docker Compose 拉起完整容器化 stack，契约测试通过后才把项目落盘**；`--no-verify` 可跳过。
4. **问卷新增 database（none/postgres）与 cache（none/redis）**，所有角色发布契约并参与求解。

## 总体架构

```
问卷选择 → 临时目录(.cgapp-tmp-<rand>/) clone/脚手架
        → 加载契约 catalog（内嵌 YAML + 模板自带 cgapp.contract.yaml 发现/覆盖）
        → CSP 求解（requires↔offers、semver、端口唯一、env 供需、auth/CORS、runtime、health）
        → 生成 glue（.env、前端 env、hosts.ini、playbook、roles 参数、
                    docker-compose.verify.yml、契约测试清单）
        → verify：docker compose up → 等 health → 迁移 → HTTP/TCP 契约探针 → down
        → 全部通过：rename 落盘到 cwd；失败：打印冲突/容器日志，清理容器，保留临时目录供排查
```

## Files and Modules

### 新增

- `pkg/contract/types.go`：契约模型（schema 版本 `cgapp.io/v1`）：
  - `Contract{APIVersion, Kind, Name, Version, Runtime, Ports, Routes, Auth, Env, Health, Resources, Requires, Offers}`
  - `Kind ∈ backend|frontend|database|cache|proxy|deploy`
  - `Runtime`：语言/版本约束（`go >=1.23`、`node >=18`）、基础镜像；
  - `Port`：名称、容器端口、默认宿主端口、协议、是否必须发布；
  - `Route`：API base path（如 `/api/v1`）、health 路径与期望状态码；
  - `Auth`：机制（none/jwt/basic/session）、CORS 能力与可配置项；
  - `Env`：`consumes`（必需/可选、默认值、是否 secret）与 `produces`（如 postgres 产出 `DATABASE_URL` 模板）；
  - `Health`：检查类型（http/tcp/exec）、目标、间隔/超时/重试；
  - `Resources`：Dockerfile 路径、构建命令、迁移目录、CPU/内存；
  - `Requirement{Capability, VersionRange, Attrs}` / `Offer{Capability, Version, Attrs}`（如 `database{driver:postgres}`、`http-api{basePath}`、`http-upstream`、`ingress{tls,acme}`）。
- `pkg/contract/load.go`：`//go:embed catalog/*.yaml` 加载多文档 YAML；从指定目录发现 `cgapp.contract.yaml`（同名以内嵌为默认、模板自带覆盖）；schema 校验（apiVersion/kind/name 必填、版本可解析）。
- `pkg/contract/catalog/*.yaml`：内置契约 —— 3 个 backend、前端按脚手架族归并为多文档（vite 纯 JS/TS、react 系、next、nuxt、sveltekit）、postgres、redis、nginx、traefik、traefik-acme-dns、deploy-ansible。**实现时须先对照上游模板仓库（fiber-go-template 等）核实端口、env 名、health 路由、迁移目录的真实值**，通过 WebFetch 核对后再落契约。
- `pkg/solver/solver.go`：回溯 CSP。
  - 输入：各 kind 选中的组件 id + 候选契约集（可能含多版本）；
  - 约束：kind 唯一；`Requires` 必须被某组件 `Offers` 满足（属性相等 + semver 区间）；宿主发布端口全局唯一（冲突时按候选端口集重新分配）；env consumer 必须有 producer 或默认值；runtime 版本兼容；frontend 期望的 API base path 必须在 backend `Routes` 中且 proxy routing 覆盖；browser 跨域时 backend 必须声明 CORS 能力（由 glue 注入 env）；每个部署组件必须声明 `Health`；
  - 输出：`Solution{Contracts, Bindings(端口/env/凭据/容器名/版本), Wirings, Diagnostics}`；不可解时返回结构化 `Conflict`（哪个组件、哪条 require、缺什么能力），供 CLI 友好输出。
- `pkg/solver/solver_test.go`：表驱动 —— 全选兼容组合可解；端口冲突可重分配；缺 database offer 不可解且报因；loose 模式下自定义无契约仅告警。
- `pkg/glue/glue.go`：由 `Solution.Bindings` 渲染/合并：
  - `backend/.env`（仅写入 backend 契约 declares 的键，合并不去注释）；
  - `frontend/.env.local`（按前端契约的变量名约定：`VITE_*`/`NEXT_PUBLIC_*`/`NUXT_*`，值为经 proxy 或直连 backend 的 API base URL）；
  - `hosts.ini`、`playbook.yml`（条件角色、全部端口/版本/容器名/迁移路径来自 bindings）；
  - `docker-compose.verify.yml` + `verify-manifest.yaml`（探针清单：backend health/API 路径、DB/缓存 TCP、迁移 one-shot、proxy 路由与 CORS 探测）。
- `pkg/registry/verify/docker-compose.verify.yml.tmpl`、`nginx.verify.conf.j2`、`traefik.verify.yml.j2`：验证专用模板（traefik 验证配置仅开 80 入口、关 ACME/重定向，用 labels 路由，见风险）。
- `pkg/verify/compose.go`：探测 `docker compose`（v2 插件优先，回退 `docker-compose`），封装 up/down/logs（带工作目录）。
- `pkg/verify/run.go`：编排 —— 生成临时 compose → `up -d --build` → 按 `Health` 轮询等待（Go 原生 net/http、net.Dial，exec 类用 `docker compose exec`）→ 迁移服务 exit 0 → 执行 verify-manifest 探针（含 Host 头穿 proxy、Origin 头验 CORS）→ 汇总 → 始终 `down -v`；失败时抓取各服务日志。
- `pkg/verify/run_test.go`：探针逻辑用 `httptest` 单测；真实 compose 链路用集成测试，`CGAPP_TEST_DOCKER=1` 时才运行。

### 修改

- [pkg/registry/defaults.go](file:///Users/goodman/swe-project/cli_fork3/pkg/registry/defaults.go)：
  - `CreateAnswers` 增加 `Database`、`Cache`；两套问卷各增加两个 Select 问题（`none/postgres`、`none/redis`）。
- [cmd/root.go](file:///Users/goodman/swe-project/cli_fork3/cmd/root.go)：新增 `database, cache string`、`noVerify bool`。
- [cmd/create.go](file:///Users/goodman/swe-project/cli_fork3/cmd/create.go)：重构为上文"总体架构"的两阶段流程（临时目录 → 发现/求解/glue/verify → 落盘）；`create` 增加 `--no-verify` flag；不可解或验证失败时在任何文件落盘前中止。
- [pkg/cgapp/git.go](file:///Users/goodman/swe-project/cli_fork3/pkg/cgapp/git.go)：新增 `GitCloneTo(folder, url)`，旧 `GitClone` 委托它。
- [pkg/cgapp/exec.go](file:///Users/goodman/swe-project/cli_fork3/pkg/cgapp/exec.go)：新增 `ExecCommandInDir(dir, command, options, silent)`（设置 `cmd.Dir`，复用输出风格），verify 包使用。
- [playbook.yml.tmpl](file:///Users/goodman/swe-project/cli_fork3/pkg/registry/templates/playbook.yml.tmpl)：postgres/redis 角色行改为 `{{ if ne .Database "none" }}` / `{{ if ne .Cache "none" }}` 条件。
- [hosts.ini.tmpl](file:///Users/goodman/swe-project/cli_fork3/pkg/registry/templates/hosts.ini.tmpl)：各段按选择条件渲染；值全部来自 bindings（backend/postgres/redis 端口与版本、容器名、迁移路径变量 `backend_migrations_path`、CORS origins）。
- roles 参数化（仍由 Ansible 部署时使用，值与 verify 同源）：
  - [backend/tasks/main.yml](file:///Users/goodman/swe-project/cli_fork3/pkg/registry/roles/backend/tasks/main.yml)：health path、traefik router prefix、容器 healthcheck 来自变量；
  - [postgres/tasks/main.yml](file:///Users/goodman/swe-project/cli_fork3/pkg/registry/roles/postgres/tasks/main.yml)：迁移挂载路径改用 `backend_migrations_path`，增加 pg healthcheck；
  - [redis/tasks/main.yml](file:///Users/goodman/swe-project/cli_fork3/pkg/registry/roles/redis/tasks/main.yml)：增加 redis healthcheck；
  - nginx/traefik 模板：upstream、路由前缀、CORS 头由 bindings 驱动。
- [cmd/deploy.go](file:///Users/goodman/swe-project/cli_fork3/cmd/deploy.go)：执行前增加轻量 preflight（本机 `ansible-playbook` 是否存在、`playbook.yml`/`hosts.ini` 是否由带契约版本生成），不改变部署行为。

### 依赖

- 新增 `gopkg.in/y.v3`（解析契约 YAML，当前 go.mod 无 YAML 库；小型标准依赖）。
- 新增 `golang.org/x/mod/semver`（官方轻量 semver，纯 Go）。
- 无 SAT/SMT、无 Docker SDK —— compose 通过现有 exec 方式调用。

## Implementation Steps

1. **契约模型与加载器**：`pkg/contract` 类型 + loader + 单测（含多文档 YAML、目录发现、覆盖规则、schema 错误）。
2. **核实上游事实**：WebFetch 核对 3 个官方 backend 模板（fiber/chi/nethttp）的监听端口、env 变量名、health 路由、迁移目录；据此编写 `catalog/*.yaml`（前端契约按脚手架族编写 env 命名约定与 node runtime）。
3. **CSP 求解器**：`pkg/solver` + 表驱动单测（兼容可解、冲突不可解带诊断、端口重分配、loose 模式）。
4. **问卷扩展**：`CreateAnswers` + database/cache 问题（默认与自定义两套问卷）。
5. **glue 生成器**：bindings → env 合并、inventory/playbook 变量模型；新增 verify 专用 compose/nginx/traefik 模板；单测（golden 文件比对渲染结果）。
6. **模板与 roles 参数化**：条件角色、bindings 变量、healthcheck、迁移路径变量、路由前缀/CORS。
7. **临时目录流程重构**：`GitCloneTo`、`ExecCommandInDir`；create.go 改为 cwd 下 `.cgapp-tmp-<rand>/` 工作（保证同文件系统 rename），失败清理容器、不落盘。
8. **verify 执行器**：docker/compose 探测、up、health 等待、迁移等待、HTTP/TCP/CORS/路由探针、日志收集、down。
9. **deploy preflight**：ansible-playbook 存在性与产物版本提示。
10. **收尾**：`gofmt`、`go vet`、全量 `go test ./...`；按 Makefile 跑 gosec/gocritic；手工矩阵验证（下节）。

## Dependencies and Considerations

- **后端/前端真实内容来自外部仓库与 npx**：内嵌契约是对上游模板的"事实声明"，存在漂移风险；模板可通过自带 `cgapp.contract.yaml` 覆盖；catalog 注释标注核实过的上游版本。
- **临时目录放在 cwd 下**（`.cgapp-tmp-*`，结束即删；`.gitignore` 增加该模式），保证 `os.Rename` 落盘不跨文件系统；落盘目标已存在时中止并提示。
- **前端不进入 compose stack**：vite/next 脚手架不带 Dockerfile；临时验证覆盖容器化单元（backend+db+cache+proxy+迁移），前端侧做静态契约校验（env 变量名、node runtime、`npm run build` 不纳入默认验证以免耗时，作为后续可选项）。
- **宿主端口动态分配**：默认端口被占用时，求解器在契约候选端口集中挑选空闲端口写入 bindings，避免与本机服务冲突。
- **自定义模板缺契约**：loose 模式 —— 该组件不参与硬约束，CLI 输出 `[INFO]` 告警并仍允许 `--no-verify` 落盘；有契约组件之间的约束照常生效。
- **凭据**：verify 使用一次性随机弱口令，仅存在于临时目录，落盘时 hosts.ini 沿用现有占位密码并保留"请填入凭据"提示。

## Validation

- 单元测试：contract loader、solver（含不可解诊断）、glue golden 渲染、verify 探针（httptest）。
- `go build ./...`、`go vet ./...`、`go test ./...` 全绿；`make security`（gosec）与 gocritic 无新增问题。
- 手工矩阵（本机 Docker，CGAPP_TEST_DOCKER=1）：
  1. `fiber + react-ts + postgres + redis + traefik`：求解通过 → compose 验证（迁移 0 退出、backend health 200、traefik Host 路由 200、CORS 头正确）→ 落盘；
  2. `net/http + none + none + none`：无 db/cache/proxy，playbook 不含相关角色，backend health 通过；
  3. 人为制造冲突（构造自定义 backend 契约 require 不存在的 db driver）：CLI 在落盘前报结构化 Conflict；
  4. `--no-verify`：跳过 Docker，直接生成；无 Docker daemon 时给出明确指引。

## Risks

- **上游模板漂移导致契约失真**：以核实过的模板版本为准并在 catalog 标注；支持模板自带契约覆盖；验证步骤实测兜底（失真会在契约测试阶段暴露，而非部署后）。
- **Traefik ACME/TLS 无法在离线临时环境完整验证**：verify 配置仅用 80 入口 + labels 验证路由/path/CORS；证书链属真实部署环境范畴，在计划与输出信息中明确该边界。
- **本机无 Docker / 镜像拉取慢**：启动前探测 daemon 并给清晰错误；compose up 设可配置超时；`--no-verify` 逃生；失败时输出镜像拉取/容器日志并保留临时目录路径。
- **脚手架网络波动（npx/npm）**：现状固有；重构后在临时目录执行，失败不会污染 cwd，行为优于现状。
- **YAML/exec 注入面**：compose 与 env 值经 text/template 写入临时目录，组件 id 来自固定选项或用户 URL；自定义输入做白名单字符校验，避免把任意字符串拼进 shell（exec 走 `exec.Command` 参数数组，不经 shell）。
