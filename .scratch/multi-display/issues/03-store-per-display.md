# 03 store 按显示器拆分

Status: ready-for-agent
Blocked by: 01

## 背景

`store`（[store.go:52](../../../store.go#L52)）把一台显示器的全部状态摊在结构体里：`monitor`、`modes`、`current`、`link`、`inputs`、`ddc`、`controls`、`brightness`……多屏后要按显示器各存一份，并引入 `target`。面板、快捷键、HUD 统一从目标读。

## 要做

1. 抽出 `displayState`，容纳现在这些字段：显示器信息、模式、当前模式、局域网连接、输入源、音量、DDC、controls、maxima、硬件亮度、跟随内屏的运行时状态、自动切换的上次时段。
2. `store` 保留全局字段（prefs、tracker、update、recording、notice 等），新增：
   - `displays map[string]*displayState`（键为身份）、`order []string`、`target string`；
   - `targetState()` 取当前目标，目标不在时按 spec 回退。
3. `refreshDisplay` / `displaysChanged` 改为重建列表：已有身份的状态原地保留，新出现的建立，消失的丢弃——**设置**由 04 负责保留，运行时状态可以丢。
4. 探测策略：目标立即探测（`detect`），其余进后台串行队列；`detectingAny` 要能按显示器区分（切换器显示"检测中"）。
5. 所有按显示器的方法（`apply`、`stepBrightness`、`setVolume`、`applyMode`、`setCompetitive`、`followTick`、`autoCheck`……）改成作用于传入的 `*displayState`；对外仍保留"作用于目标"的薄封装，避免调用点全改。

## 验收

- `panel_test.go` 的 `testPanel` 只造一个 `displayState` 也能跑；现有测试语义不变。
- 新增：两台显示器时切换 `target` 后，`setVolume`/`stepBrightness` 只改对应那台。
- 三台外接时构造 store 不阻塞，非目标显示器状态标记为探测中。

## 备注

这一步只做结构拆分，不要顺手改设置存储格式（那是 04）。`followTick` 目前每秒跑一次，多台时要确认总写入量可接受（每台每 tick 最多一次 DDC 写）。
