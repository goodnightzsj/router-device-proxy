# Router Device Proxy

按设备选择 **直连** 或 **透明代理** 的轻量 Web 控制台。默认设备继续走主路由直连，只有被标记的设备通过旁路由（OpenClash / Clash）上网；旁路由故障不会影响直连设备。

![screenshot](docs/screenshot.png)

## 功能

- 自动扫描路由器下的设备：名称、品牌、MAC、IP、在线状态
- 按设备切换 `直连 / 透明代理`
- 通过 dnsmasq 的 `dhcp-host` + `dhcp-option` 下发网关/DNS，设备续租后生效
- 策略持久化：容器数据卷 + 路由器 `/data` + 开机 hook
- 单密码登录；路由器 SSH 密码/私钥加密保存在数据卷
- 单二进制 + 嵌入前端，Docker 镜像基于 `scratch`，约 8–10MB

## 工作原理

```text
浏览器
  │
  ▼
:8090  Go 单二进制（嵌入 UI）
  ├─ 设备扫描：SSH（dnsmasq leases + ip neigh）
  ├─ 策略：/data/policy.json
  └─ SSH → 路由器
        ├─ /data/dnsmasq.d/proxy.conf   （持久）
        ├─ /tmp/dnsmasq.d/proxy.conf    （生效）
        └─ /etc/init.d/dnsmasq restart
```

代理设备会得到：

```conf
dhcp-host=AA:BB:CC:DD:EE:FF,set:proxy,192.168.31.50
dhcp-option=tag:proxy,3,192.168.31.122
dhcp-option=tag:proxy,6,192.168.31.122
```

未标记设备继续使用主路由下发的网关/DNS，直连不受旁路由影响。

## 快速开始

```bash
cp .env.example .env
# 修改 .env 里的 ADMIN_PASSWORD
docker compose up -d --build
```

打开 `http://<运行主机>:8090`，输入 `ADMIN_PASSWORD` 登录。

### 直接使用 docker run

```bash
docker run -d \
  --name router-device-proxy \
  --restart always \
  -p 18099:8090 \
  -e TZ=Asia/Shanghai \
  -e ADMIN_PASSWORD="$(cat /path/to/admin-password.txt)" \
  -e ROUTER_HOST=192.168.31.1 \
  -e ROUTER_USER=root \
  -e ROUTER_PASSWORD='你的路由器 SSH 密码' \
  -e PROXY_GATEWAY=192.168.31.122 \
  -e ROUTER_LEASE_FILE=/tmp/dhcp.leases \
  -v /path/to/data:/data \
  <DOCKERHUB_USERNAME>/router-device-proxy:latest
```

容器内监听 `8090`，宿主机端口可以换成任意空闲端口。也可以直接 `-e ADMIN_PASSWORD='...'`，不一定要密码文件。

首次登录后点右上角 **⚙ 设置**：

| 字段 | 说明 |
|---|---|
| 路由器 IP | 主路由地址，例如 `192.168.31.1` |
| SSH 用户 | 通常是 `root` |
| 代理网关 | 旁路由地址，例如 `192.168.31.122` |
| DHCP leases 路径 | 默认 `/tmp/dhcp.leases`；不同固件可改 |
| SSH 密码 / SSH 私钥 | 二选一；保存后加密存放在数据卷，不需要每次输入 |

点 **测试连接** 验证 SSH，点 **安装开机持久化** 在路由器上写入 `/data/dnsmasq.d/restore.sh` 并挂到防火墙 include，重启后自动恢复。

## 环境变量

| 变量 | 默认 | 说明 |
|---|---|---|
| `ADMIN_PASSWORD` | 必填 | Web 登录密码 |
| `ADDR` | `:8090` | 监听地址 |
| `DATA_DIR` | `/data` | 数据卷 |
| `ROUTER_HOST` | `192.168.31.1` | 主路由 IP |
| `ROUTER_USER` | `root` | SSH 用户 |
| `ROUTER_PASSWORD` | 空 | SSH 密码（也可在设置页保存） |
| `ROUTER_KEY` | 空 | SSH 私钥内容（也可在设置页保存） |
| `ROUTER_LEASE_FILE` | `/tmp/dhcp.leases` | dnsmasq 租约文件路径 |
| `PROXY_GATEWAY` | `192.168.31.122` | 旁路由网关 |
| `ROUTER_CONF_DIR` | `/data/dnsmasq.d` | 路由器持久配置目录 |
| `ROUTER_TMP_DIR` | `/tmp/dnsmasq.d` | dnsmasq 实际读取目录 |
| `ROUTER_RESTART_CMD` | `/etc/init.d/dnsmasq restart` | 应用后重启 dnsmasq 的命令 |
| `MOCK` | `false` | 本地 UI 调试模式，不连路由器 |

## 路由器要求

- 路由器可通过 SSH 读取 dnsmasq leases（默认 `/tmp/dhcp.leases`）和 `ip neigh` / `/proc/net/arp`
- dnsmasq 使用 `conf-dir=/tmp/dnsmasq.d`（OpenWrt 默认符合；其他固件可改 `ROUTER_CONF_DIR` / `ROUTER_TMP_DIR`）
- 路由器支持写 `/data` 或等效持久分区
- 旁路由已配置透明代理和 DNS（本项目只负责下发网关/DNS，不修改 OpenClash 配置）

## 安全说明

- 只监听 LAN，不要把端口暴露到公网
- Web 使用单密码 + HttpOnly 会话 cookie
- 路由器密码/私钥使用 AES-GCM 加密，密钥由 `ADMIN_PASSWORD` 派生
- SSH 使用 `InsecureIgnoreHostKey`，适合可信 LAN；如需严格校验可自行扩展
- 容器以 root 运行以写数据卷；如要降权，请自行调整卷权限

## 本地开发

```bash
ADMIN_PASSWORD=test MOCK=1 DATA_DIR=./data go run .
```

打开 `http://127.0.0.1:8090`，密码 `test`，会看到 6 台模拟设备。

```bash
go test ./...
go build -trimpath -ldflags="-s -w" -o rdp .
```

## 镜像与 GitHub Actions

`.github/workflows/docker.yml` 在 push 到 `main` 或 `v*` tag 时构建并推送：

- `DOCKERHUB_USERNAME/router-device-proxy:latest`
- `DOCKERHUB_USERNAME/router-device-proxy:<sha>`

需要在仓库 Secrets 中设置：

- `DOCKERHUB_USERNAME`
- `DOCKERHUB_TOKEN`

## 限制

- DHCP 下发需要设备重连/续租后生效
- 目前只处理 IPv4；IPv6 分流未实现
- 开机持久化 hook 面向 OpenWrt/dnsmasq；其他系统需自行适配
- 品牌识别依赖 OUI 库，未知设备显示 `Unknown`

## License

MIT
