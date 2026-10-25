# 多品牌路由器兼容架构

按 dev-guide 的 Go 分层与依赖方向原则设计：消费方拥有窄接口、`internal/` 保护实现、`main` 只做装配、按真实变化轴拆边界，不为目录数或“未来可能”增加抽象。

## 1. 目标与非目标

**目标**

- 同一个 Web UI、策略存储和名称合并逻辑支持多个路由器品牌。
- 品牌差异（设备表来源、名称来源、下发方式、持久化方式）隔离在适配器内。
- 保持单二进制、`scratch` 镜像和现有部署方式。
- 新品牌接入只新增适配器包和工厂分支，不改核心流程。

**非目标**

- 不做动态插件加载、独立进程或微服务。
- 不统一各品牌的私有云接口。
- 不在本轮实现 IPv6 分流。

## 2. 当前变化轴

| 变化轴 | 当前实现 | 其他品牌可能不同 |
|---|---|---|
| 设备表 | SSH 读 dnsmasq leases + `ip neigh` + `/proc/net/arp` | 厂商 HTTP API、SNMP、专有客户端列表 |
| 名称来源 | DHCP、DNS/NetBIOS/mDNS/LLMNR/SSDP、小米 xqDb/API | 厂商设备库、云账号、本地别名 |
| 策略下发 | 写 dnsmasq `dhcp-host` / `dhcp-option` | 厂商 DHCP API、静态租约、ACL |
| 持久化 | 路由器 `/data` + firewall include | 厂商配置保存、启动脚本、无持久化 |
| 健康检查 | SSH `true` | HTTP 登录、SNMP、厂商状态接口 |

## 3. 当前问题

- `package main` 同时承担 HTTP、会话、策略存储、SSH、dnsmasq 渲染、小米 xqDb/API 和名称合并；新增品牌会改到核心文件。
- 小米名称有两条路径（xqDb 与 Web API），优先级和失败回退只存在于实现细节里，没有显式合同。
- 策略渲染假设 dnsmasq；非 OpenWrt 品牌无法复用，也无法在 UI 上表达“此品牌不支持下发”。
- 设备/名称/策略/持久化被当成一个整体，实际是四个可独立变化的能力。

## 4. 目标边界

```text
cmd/router-device-proxy/main.go      # 只做配置、装配、启动、关闭
internal/app/                        # HTTP 路由、会话、UI 数据组装
internal/device/                     # Device 模型、名称优先级、发现编排、缓存
internal/router/                     # 端口定义（由消费方拥有）
internal/router/openwrt/             # SSH dnsmasq + ARP + 策略渲染 + restore hook
internal/router/xiaomi/              # xqDb 只读名称源 + 可选 Web API；复用 openwrt 设备源
internal/router/generic/             # SSH leases/ARP，只读，不下发
internal/sshx/                       # 共享 SSH 传输与超时
internal/web/                        # 嵌入的静态资源
```

### 端口：窄接口 + 可选能力

```go
type DeviceSource interface {
    Devices(ctx context.Context) ([]Device, error)
}

type NameSource interface {
    Names(ctx context.Context) (map[string]string, error)
}

type PolicyApplier interface {
    Apply(ctx context.Context, policies []Policy) error
}

type PersistenceInstaller interface {
    Install(ctx context.Context) error
}

type CapabilityReporter interface {
    Capabilities() Capabilities
}
```

- 核心只依赖这些能力，不依赖“路由器”这个大接口。
- 品牌适配器按实际能力实现一个或多个接口；不支持的策略下发通过 `ErrUnsupported` 明确返回，UI 显示为不可用，而不是静默失败。
- 小米适配器组合 `openwrt.DeviceSource` 与自己的 `NameSource`，用组合而不是继承。
- 接口定义在 `internal/router`（消费方），实现放在各品牌包；避免核心反向依赖品牌包。

### 名称优先级

核心拥有合并规则，适配器只返回事实：

```text
手动别名 > 厂商设备库/API > DHCP hostname > DNS PTR > NetBIOS > mDNS > LLMNR > SSDP > OUI 厂商
```

- 空值、`Android`、`localhost`、MAC 本身等通用名不参与覆盖。
- 每个来源带 TTL 与失败退避；失败只降级该来源，不阻塞其他来源。
- 手动别名是最后兜底，也是通用品牌无法识别设备时的唯一可靠方案。

### 策略与持久化

- `policy.Store` 留在核心，负责用户选择与持久化。
- `openwrt` 适配器负责把策略渲染成 dnsmasq 配置、写入 `/data` 并安装 restore hook。
- 其他品牌实现自己的 `PolicyApplier`；不支持的品牌只展示设备与名称，不展示“应用更改”或明确禁用。
- 策略应用必须幂等；重复应用不产生重复规则。

## 5. 品牌选择

- 配置：`ROUTER_BRAND=auto|xiaomi|openwrt|generic`。
- `auto` 只做能力探测，不做猜测式写入：
  1. xqDb 可读 → 启用 Xiaomi 名称源；
  2. dnsmasq lease 可读 → 启用 OpenWrt/generic 设备源；
  3. 策略下发能力单独探测，探测失败不等于设备扫描失败。
- 使用编译期注册表，不引入动态插件：

```go
func New(cfg Config) (router.Set, error) {
    switch cfg.Brand {
    case "xiaomi":
        return xiaomi.New(cfg)
    case "openwrt":
        return openwrt.New(cfg)
    default:
        return generic.New(cfg)
    }
}
```

新增品牌 = 新包 + 工厂分支 + 合同测试，不修改核心 HTTP 或名称合并代码。

## 6. 迁移顺序

**Phase 0（已完成）**

- 小米 xqDb 只读回退，解决 Web API 401 时拿不到名称。
- 离线设备在前端列表隐藏。
- 可清除已保存的小米后台密码。

**Phase 1：提取模型与端口，不改行为**

- 把 `Device`、`Policy`、名称优先级和缓存移到 `internal/device` / `internal/router`。
- 现有 SSH/dnsmasq 代码包一层适配器，保持 `/api/devices` 输出不变。
- 加适配器合同测试（假 SSH/HTTP）和 `/api/devices` golden 对比。

**Phase 2：拆 OpenWrt / generic**

- 把 dnsmasq 渲染、`/data` 写入、restore hook 移到 `openwrt`。
- generic 只保留设备/名称读取，`PolicyApplier` 返回 `ErrUnsupported`。
- `main` 只保留装配。

**Phase 3：拆 Xiaomi 与能力展示**

- xqDb 解析器和 Web API 移到 `xiaomi`；保留只读、TTL、失败退避。
- `/api/status` 增加 `capabilities`，UI 按能力显示或禁用“应用更改”。
- 名称来源可在 UI 上以标签展示（可选）。

**Phase 4：用第二个品牌验证边界**

- 选一个真实可测的品牌（OpenWrt 原生或厂商 HTTP API）接入。
- 只有第二个实现证明接口确实稳定后，再考虑第三个品牌；不提前做插件框架。

## 7. 验收

- `go test -race ./...`、`go vet ./...`、`gofmt -l .` 干净。
- 适配器合同测试：设备为空、SSH 超时、认证失败、未知字段、重复应用策略。
- xqDb 解析器用真实数据库验证过：27 个名称、手机 MAC 名称与 `sqlite3` 查询一致。
- 迁移前后同一路由器的 `/api/devices` 对比：设备数、MAC、名称、在线状态、类型一致。
- 真实入口冒烟：页面加载、刷新、设置保存、策略应用、容器重启后恢复。
- 失败路径：xqDb 不存在、Web API 401、策略不支持、路由器离线，均有明确降级且不阻塞设备列表。

## 8. 代价与风险

- 包拆分增加文件与装配代码；只有真实品牌变化轴成立时才值得，不为“看起来分层”继续加层。
- 厂商固件/API 会漂移；适配器隔离变化，但需要真实设备回归，不能只靠 mock。
- 名称优先级是产品决策；必须显式、可配置，不能藏在某个适配器的 if 里。
- 策略下发风险最高：不同品牌的幂等性、持久化和失败恢复不同，必须逐品牌验证。
- 保持镜像小：xqDb 用只读最小解析器，不引入 SQLite 驱动；新增品牌优先用标准库 HTTP/SSH。

## 9. 结论

采用**模块化单体 + 编译期品牌注册表 + 窄能力接口**，不引入插件进程或微服务。先完成 Phase 1 的模型/端口提取，再用第二个品牌验证接口；核心始终拥有名称优先级、策略存储和 UI，品牌适配器只提供事实与能力。
