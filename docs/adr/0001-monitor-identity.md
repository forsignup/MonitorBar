# 显示器身份用 EDID，位置键只在重复时兜底

多屏之后每台显示器的设置要跨启动、跨重插保留，因此需要一个稳定身份。系统的显示句柄（`CGDirectDisplayID`、`\\?\DISPLAY1`）在重启、重插、睡眠唤醒后都会变，不能用作键。我们以 EDID 的厂商号/型号号/序列号为主键；只有出现重复（同型号、序列号为 0，或多台同型号同时在线）时才追加位置键——macOS 用 `CGDisplayCreateUUIDFromDisplayID`，Windows 用 `DISPLAYCONFIG_TARGET_DEVICE_NAME.monitorDevicePath`——并在查找时先精确匹配、再退回主键唯一匹配。

选择面板优先而不是位置优先，是因为设置描述的是「这台面板的亮度和色温」，换到另一个接口应该跟着显示器走；若反过来按接口存，把线重插一次就全丢。代价是：同型号且无序列号的显示器换接口后，位置键变了，那台的历史设置不再匹配——这是可接受的边界，写进 README 的已知限制。

## Considered Options

- **系统不透明 ID（display UUID / 设备路径）**：实现最省事，但跨重启与跨系统版本的稳定性由系统决定，也没有可读名称，迁移与排障都更难。
- **严格按接口位置**：同型号绝不冲突，但换线即换设置。
- **手动命名并指定**：最可控，但每次换线要人工确认，用户不接受的成本。

## Consequences

- macOS 的位置键用 ColorSync 的公开 API；Apple Silicon 上按显示器绑定 DDC 还需要 `IODisplayLocation`（私有），那是 ADR-0002 的事，不要混进身份。
- 身份字符串要能一眼看出是不是位置键，`identity` 的格式与迁移日志要稳定。
