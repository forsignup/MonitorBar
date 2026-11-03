# macOS 多屏 DDC 用私有 API 按 EDID 绑定 AV service

macOS 没有公开 API 把 `CGDirectDisplayID` 与 `DCPAVServiceProxy`（DDC/CI 的 I2C 通道）对应起来。现在的实现直接取第一个 `Location == External` 的 AV service，等于假设只有一台外接。多屏后必须真正绑定：用未文档化的 `IOAVServiceCopyEDID` 读出每个 AV service 的 EDID，与 `CGDisplayVendorNumber/ModelNumber/SerialNumber` 比对；同型号重复时再用 `IODisplayLocation` 在 IORegistry 里定位对应的 `DCPAVServiceProxy`（m1ddc / MonitorControl / Lunar 都是这个思路）。两个 API 都通过 `dlsym` 调用，失败时不做贪心配对，而是把该台标记为"硬件控制不可用"、退回软件调光。

这偏离了"只用公开 API"的直觉，但替代方案是 macOS 多屏下没有硬件亮度与 DDC 画面控制；项目已经在用 `IOAVService*`、`IOFBCopyI2CInterfaceForBus`、`DisplayServices` 等未文档化接口，风险与既有实现一致。

## Consequences

- 私有 API 在 macOS 大版本间可能变化，必须能优雅降级，且 `--probe` 要能看出绑定失败的原因。
- 同型号且序列号为 0 的显示器无法保证配准；这条约束直接决定了"绑定失败就报不可用"的产品行为。
- 该方案只在单外接机器上验证过，双外接需要真机验收（见 issue 09）。
- 绑定结果要缓存，并在 `CGDisplayRegisterReconfigurationCallback` / 显示器配置变化时重建；唤醒后要重新枚举，避免陈旧 service 让 DDC 卡住。
