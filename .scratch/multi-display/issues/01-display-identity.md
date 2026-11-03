# 01 显示器发现与身份

Status: ready-for-agent
Blocked by: —

## 背景

`display.Target()`（[display_darwin.go:146](../../../internal/display/display_darwin.go#L146)、[display_windows.go:144](../../../internal/display/display_windows.go#L144)）只返回一台；`gammaBase`（[display_darwin.go:253](../../../internal/display/display_darwin.go#L253)）与 `gammaDevice`（[display_windows.go:297](../../../internal/display/display_windows.go#L297)）只存一份。身份规则见 [spec](../spec.md#显示器发现与身份) 与 [ADR-0001](../../../docs/adr/0001-monitor-identity.md)。

## 要做

1. `Monitor` 增加 `Identity string` 与身份字段（供应商、型号、序列号、位置键）。身份的拼接与"重复时追加位置键"的判定抽成纯函数，便于单测。
2. 新增 `All() []Monitor`：只返回外接显示器，排除镜像副本与虚拟显示器。
   - macOS：`CGGetOnlineDisplayList` + `CGDisplayIsBuiltin` 过滤；位置键用 `CGDisplayCreateUUIDFromDisplayID`；名称沿用 `screenName`。
   - Windows：`EnumDisplayDevices` 遍历全部；位置键取 `DISPLAYCONFIG_TARGET_DEVICE_NAME.monitorDevicePath`（结构体已在 [display_windows.go:124](../../../internal/display/display_windows.go#L124) 声明）。
3. 排序：主显示器在前，其余按屏幕坐标从左到右。macOS 用 `CGDisplayBounds`，Windows 用 `DEVMODE.PositionX/PositionY`。
4. Gamma 按身份各存一份：`ResetGammaBase`、`RestoreGamma` 改为逐台（Windows 的 `gammaDevice` 改成 map）；退出时恢复全部。
5. `Connection`（接口类型）尽量按台取；取不到就留空，不要回退到"全局第一个"（现状 [display_darwin.go:176](../../../internal/display/display_darwin.go#L176) 会给出别的显示器的接口）。

## 验收

- 单测覆盖身份拼接：无重复、序列号为 0、两台同型号同时在线、换接口后重新匹配。
- 两台外接时 `All()` 返回两台，顺序与系统设置一致；只有内屏时返回空。
- 单台外接的既有行为不变。
- macOS / Windows 都能编译，`go test ./...` 通过。

## 备注

`Connection` 无法按台取时不要硬凑——错误接口名比空字符串更糟。接口信息在有多台时没有它也能从名称与位置键区分。

## Comments

- 2026-11-03：macOS 多台外接时 `Connection` 留空：IOKit 的端口传输服务没有公开的「属于哪块 CGDisplay」对应关系，按 issue 备注不回退到全局第一个。切换器在名称相同时改用位置键的后 8 位区分，位置键也没有时用系统顺序编号。
