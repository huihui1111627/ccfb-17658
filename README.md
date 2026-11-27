# 裸金属装机流程仿真与分批调度控制台

用 Go 标准库实现的 800 台服务器网络装机仿真器。它模拟服务器 UEFI/PXE、网卡固件、DHCP/IPAM、TFTP、HTTP Boot、镜像服务、RAID 模板和机柜交换机；提供容量瓶颈计算、分批上电节奏、硬件/RAID 预检、根因归因、真值核对，以及嵌入式 Web 控制台。

无第三方 Go 依赖。

## 快速开始

```bash
# 启动页面和 API，默认 :8080
go run ./cmd/rollout

# 换端口 / 种子 / 机器数
go run ./cmd/rollout --addr 127.0.0.1:18080 --machines 800 --seed 20261127

# 只看命令行对比报告
go run ./cmd/rollout --cli

# 测试
go test ./...
```

打开 <http://localhost:8080/>。

## 页面能力

- **实时装机**：KPI、16 段启动流水线计数、按根因统计、800 台机器筛选表、进度/实时速率。
- **机器详情**：仿真真值、工具归因、采购单 vs 实际硬件、RAID 信息，以及带时间戳的 DHCP DORA、TFTP RRQ/OACK、HTTP Range、硬件清点、RAID/分区、重启等交互记录。
- **机柜成片问题**：20 个机柜的热力图；同一机柜多台 DHCP Discover 无响应时聚类为 ToR VLAN/配置问题，并展示交换机配置证据。
- **瓶颈与分批**：容量公式、预测利用率、全开/分批实测峰值、超时数、推荐批大小、上电速率、流水线间隔和批次机柜分布。
- **硬件 / RAID 预检**：内存 DIMM、硬盘数量/型号、网卡固件、RAID 控制器模式、模板适用性问题；异常机器不进入装系统。
- **真值逐台核对**：混淆矩阵和 800 行“埋设问题 vs 工具归因”对照，可只看偏差。

页面右上角可以暂停、调速、立即跑完，或重放“全部上电”和“建议分批”。

## 仿真的启动顺序

```text
上电/BMC → BIOS POST → 启动顺序/PXE 项 → 网卡固件/PXE ROM
→ DHCP Discover/Offer/Request/Ack → TFTP 下载 snponly/iPXE
→ HTTP 拉 kernel/initrd → 启动安装 Agent 并清点硬件
→ 采购单核对 → RAID/模板校验 → 分区/格式化
→ HTTPS 拉镜像 → 释放镜像/安装 OS → 重启
→ 硬盘进入新系统 → Agent 健康检查
```

共享服务不是简单 sleep：

- DHCP/IPAM 使用聚合服务队列，突发会产生排队和重试。
- TFTP 有会话数、建连速率、单流速度和超会话拥塞塌缩。
- HTTP Boot 与镜像服务按并发数分摊出口带宽，并对超过安全并发后的上下文/重传开销做效率折减。
- 每台机器有稳定的链路质量系数，模拟 ToR 调度、RTT/TCP 和节点差异。
- 超时后按阶段进行有限重试和指数抖动退避；镜像有总维护窗口。
- 分批方案在一批机器镜像拉取完成、进入本地安装后即放行下一批，使本地安装/重启与下一批 PXE 引导重叠。

## 800 台机队与埋设问题

默认种子 `20261127`，4 种机型按比例生成，每柜 40 台，共 20 柜。

| 仿真真值 | 数量 | 表现/检出阶段 |
|---|---:|---|
| 健康 | 564 | 全流程成功 |
| 机柜交换机未放通装机 VLAN | 80（R07、R15 各 40） | DHCP Discover 后无 Offer，按机柜聚类 |
| 启动顺序/PXE 项错误 | 24 | UEFI 启动项阶段阻塞 |
| 网卡固件/PXE ROM 过旧 | 28 | 固件兼容性检查阻塞 |
| 内存 DIMM 少插 | 32 | 硬件清点 vs 采购单 |
| 硬盘型号不符 | 40 | 硬件/认证盘清单核对 |
| RAID 控制器模式/阵列配置错误 | 24 | RAID 模板预检 |
| RAID/分区模板不适用机型 | 8 | 模板适用性校验 |

全开时容量拥塞还会产生额外的 `IMAGE_SERVICE_OVERLOAD` 终态失败；这不是埋设的硬件真值，而是调度方案导致的级联问题。

## 一次全开 vs 建议分批（默认种子示例）

命令：`go run ./cmd/rollout --cli`

| 指标 | 一次全部上电 | 建议分批 |
|---|---:|---:|
| 推荐批次 | 1 次全开 | 72 台/批，12 批，8 台/秒错峰上电 |
| 仿真收敛耗时 | 约 1h01m | 约 45m |
| 成功 | 432 | **564** |
| 硬件/配置预检阻塞 | 236 | 236 |
| 容量拥塞失败 | 132 | **0** |
| 真值归因一致率 | 668/800（132 台健康机被镜像拥塞掩盖为服务超时） | **800/800 = 100%** |

容量预测中，TFTP/引导服务约在 40 秒首先进入硬饱和，镜像服务约在 80–120 秒形成高并发瓶颈；DHCP/IPAM 在当前容量下约为 83% 利用率，出现排队但不是最早的致命点。分批后 TFTP 峰值约 25 个会话、镜像峰值约 56 路，均低于安全水位。

以上数值是确定性仿真模型的输出，用于演示容量推理和调度对比；容量参数集中在 `internal/plan/plan.go` 的 `DefaultProfile()`，可按现场压测值调整。

## HTTP API

| 方法/路径 | 说明 |
|---|---|
| `GET /api/state` | 当前实时仿真的汇总统计和全部机器视图 |
| `GET /api/machines/{id}` | 单台机器完整硬件信息和排序后的交互日志 |
| `GET /api/racks[?includeMachines=1]` | 机柜聚合、交换机配置和聚类证据 |
| `GET /api/hardware-checks` | 硬件/RAID/模板预检异常表 |
| `GET /api/comparison` | 全开 vs 分批完整报告、准确率和混淆矩阵 |
| `GET /api/plan` | 容量画像、瓶颈预测和批次计划 |
| `POST /api/live/reset?mode=batched\|storm` | 重置实时仿真 |
| `POST /api/live/pause` / `resume` | 暂停/继续 |
| `POST /api/live/speed?speed=slow\|normal\|fast` | 切换播放速度 |
| `POST /api/live/finish` | 用 1 秒内部 tick 快速跑到收敛 |

## 代码结构

```text
cmd/rollout/              可执行入口：Web 服务或 --cli 报告
internal/domain/          机器、硬件、工单、阶段、日志等领域模型
internal/fleet/           800 台机队、机型、机柜交换机、确定性埋点
internal/diagnose/        硬件/RAID 校验、根因归因、机柜聚类、准确率
internal/plan/            容量画像、瓶颈预测、批次/节奏计算
internal/sim/             状态机、服务队列、带宽分摊、超时重试、调度器
internal/report/          两种方案运行、指标汇总、对比报告
internal/api/             HTTP API 和嵌入式 Web 控制台
```

## 后续可接真实环境的点

- 将 `fleet.Generate` 替换为 CMDB/采购单导入和 Redfish/LLDP/交换机配置采集。
- 保持 `diagnose` 的规则结构，接入实际固件兼容矩阵和机型 RAID 模板。
- 用现场 DHCP/TFTP/对象存储监控指标校准 `plan.Profile`。
- 将 Webhook/工单系统接到 `Attribution` 和预检阻塞结果。
