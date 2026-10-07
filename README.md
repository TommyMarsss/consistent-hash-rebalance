# 热点感知的一致性哈希分片系统

纯 Go 标准库实现的一致性哈希分片与动态再平衡系统：一致性哈希环（虚拟节点）负责节点级路由，分片表负责 key 空间的划分与迁移，热点检测器在分片访问过热时自动拆分并迁移到多个节点。运行模拟后生成一个**单文件静态 HTML 报告**（数据与逻辑全部内嵌，原生 HTML/CSS/JS，无任何第三方依赖）。

## 运行

```bash
go run . report.html   # 跑模拟并生成报告（默认文件名 report.html）
open report.html       # 浏览器打开：哈希环分布、热力图、迁移路径
go test ./...          # 全部测试
go test -race ./...    # 含竞态检测（迁移一致性测试依赖并发读）
```

## 架构

| 文件 | 职责 |
|---|---|
| `ring.go` | 一致性哈希环：虚拟节点、增删节点、O(log n) 查找 |
| `shard.go` | 分片表：区间划分、拆分计划、原子发布、覆盖不变式校验 |
| `hotspot.go` | 热点检测（倍数阈值 + 绝对下限）与目标节点选择（最少负载优先） |
| `migration.go` | 迁移执行器：两阶段拆分 + 逐片原子切换，记录事件 |
| `sim.go` | 模拟器：Zipf 请求流 + 注入热点 + 节点增删 + 迁移比例校验 |
| `report.go` | 生成单文件静态 HTML 报告 |

### 一、一致性哈希环设计

- 哈希空间为 32 位环 `[0, 2^32)`。哈希函数为 **FNV-1a + murmur3 fmix32 终态混合**——裸 FNV-1a 对 `node-A#123`、`key-456` 这类短相似字符串扩散不足，会导致虚拟节点聚集、迁移比例偏离理论值（实测偏差可达 34σ）；加终态混合后雪崩效应完整，实测比例紧贴 1/N。
- 每个物理节点默认 **150 个虚拟节点**（`nodeID#i` 哈希后排序成环），key 顺时针落在第一个虚拟节点所属的物理节点上。
- **最小迁移性质**：增加一个节点时，只有落入新虚拟节点弧段的 key 迁移，且全部迁移**到**新节点；删除节点时只有原属该节点的 key 迁移。测试对这两条做严格断言（`UnrelatedMoves == 0`），并用 10 万 key 验证迁移比例 ≈ 1/N（±50% 容差内，实测偏差通常 <15%）。

### 二、分片与热点检测

- key 空间初始等分为 64 个**分片**（连续半开区间），每个分片的属主由环对分片中点的映射决定。路由 = `hash(key) → 分片 → 属主节点`。
- 每个 epoch 统计各分片访问量。分片为热点当且仅当：
  - 访问量 ≥ **3 × 全体分片均值**，且
  - 访问量 ≥ **1000 次绝对下限**。
- **阈值选择的理由**：3× 均值对应"显著偏离"的工程惯例（约等于假设弱偏斜下的 2σ+ 离群），既能抓住 Zipf 型热点，又不会因虚拟节点固有的负载抖动误触发；绝对下限防止系统整体空闲时（均值极小、相对比例噪声大）做无意义的拆分。两个条件缺一不可，分别有测试覆盖（`TestDetectorUniformNoHot`、`TestDetectorQuietBelowMinCount`、`TestSplitTiming`）。
- 热点分片被**拆成 3 个连续子区间**，由 `LoadBalancer` 挑选当前负载最低、且尽量不含原属主的节点作为目标，逐片迁移。拆分严格保证**无重叠无遗漏**：子区间首尾相接、并集等于原区间（`TestSplitNoOverlapNoGap` 对 2/3/5/7 份拆分逐一验证），且每次表发布后都重新校验全空间覆盖不变式。

### 三、迁移过渡期的路由一致性保证

核心机制：**路由表是不可变快照 + 原子发布**。

1. 读路径零锁：`Route()` 通过 `atomic.Pointer` 加载当前表，永远看到某一个**完整**版本，绝不会看到两个版本的混合。
2. 拆分两阶段执行：
   - **阶段 1**：用子分片替换旧分片，但属主全部保持为旧节点——路由结果不变，覆盖不变式保持；
   - **阶段 2**：对每个子分片，先在路由仍指向旧属主时完成数据拷贝，然后**一次原子发布**把该子分片属主切换为新节点。
3. 因此对任意 key，任意时刻都有**唯一**属主；其属主序列在整个迁移中**最多变化一次**，且只能是 `旧属主 → 最终属主`，不存在双写窗口或来回抖动。

`TestRoutingConsistencyDuringMigration` 在迁移进行中用 4 个并发 reader 高频查询 300 个落在被拆分区间内的 key，记录每个 key 的属主序列，断言：只出现合法属主、最多一次转移、转移方向合法；同时每个发布版本都重新校验覆盖不变式。该测试在 `go test -race` 下通过。

### 四、HTML 报告

`go run . report.html` 生成单文件报告（约 55KB，JSON 数据内嵌、Canvas 绘制、无任何外部请求）：

- **哈希环视图**：物理节点（圆点）、虚拟节点（刻度）、分片弧带（按属主着色、透明度编码热度、热点拆分产物红点标记）；点击再平衡事件可在环上叠加显示**迁移路径**（旧属主 → 各目标节点的虚线箭头）。
- **热力图**：按哈希区间排序的分片访问柱状图，热点分片红色高亮，悬停显示明细，点击柱子在环上定位该分片。
- **迁移比例校验表**：节点增删的理论比例（1/N）vs 实测比例、迁移 key 数、无关迁移数（必须为 0）及结论。

### 五、测试覆盖

| 测试 | 验证点 |
|---|---|
| `TestAddNodeMigrationRatio` / `TestRemoveNodeMigrationRatio` | 节点增删迁移比例 ≈ 1/N，且无关迁移 = 0 |
| `TestRingDeterministic` / `TestRingCoverage` | 路由确定性、全覆盖 |
| `TestInitialTableCoverage` | 初始分片表无重叠无遗漏 |
| `TestSplitNoOverlapNoGap` | 2/3/5/7 份拆分的区间划分正确性 + 表不变式 |
| `TestSplitStage1KeepsRouting` | 拆分阶段 1 不改变任何路由结果 |
| `TestCoverageUnderRepeatedSplits` | 连续 20 轮拆分后覆盖不变式仍成立 |
| `TestDetectorTriggersAboveThreshold` / `TestDetectorUniformNoHot` / `TestDetectorQuietBelowMinCount` | 检测阈值语义 |
| `TestSplitTiming` | 拆分恰好在越过阈值的 epoch 触发，不早不晚 |
| `TestPickTargetsSpreads` | 目标节点选择避开原属主、向低负载节点分散 |
| `TestRoutingConsistencyDuringMigration` | 迁移过渡期并发路由的唯一性/单调性（race 下通过） |
| `TestMigrationCompletesOwnership` / `TestMigrationDoesNotDisturbUnrelatedKeys` | 迁移完成后属主正确、无关 key 不受扰动 |
| `TestSimEndToEnd` | 端到端：热点必触发拆分、比例校验通过、报告生成 |

## 已知取舍

- 模拟器每个 epoch 最多拆分一个热点分片，避免震荡并让事件日志可读；真实系统可放宽并加冷却期。
- 分片拆分份数固定为 3（`SimConfig.SplitParts`），更精细的实现可按热度倍数自适应。
- 数据拷贝阶段用普查 key 计数模拟（`Migrator.CopyKeys`），不搬运真实数据。
