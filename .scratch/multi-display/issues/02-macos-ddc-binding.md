# 02 macOS 按显示器绑定 DDC

Status: ready-for-agent
Blocked by: 01

## 背景

`avDDCTransport()`（[ddc_darwin.go:60](../../../internal/display/ddc_darwin.go#L60)）取第一个 `Location == External` 的 `DCPAVServiceProxy`，注释里就写着"the app manages one external monitor"。两台外接时哪台响应取决于枚举顺序。Intel 的 framebuffer 路径已按 EDID 匹配（[ddc_framebuffer_darwin.go:171](../../../internal/display/ddc_framebuffer_darwin.go#L171)），可以照抄思路。方案与风险见 [ADR-0002](../../../docs/adr/0002-apple-silicon-ddc-binding.md)。

## 要做

1. 通过 `dlsym` 绑定 `IOAVServiceCopyEDID`（签名 `IOReturn(IOAVServiceRef, CFDataRef*)`），解析 256 字节 EDID：厂商号 `b[8..9]` 大端、型号号 `b[10..11]` 小端、序列号 `b[12..15]` 小端。
2. 枚举 `DCPAVServiceProxy`（`Location == External`），为每个 service 读出 EDID，建立 `displayID → service` 映射；与 `CGDisplayVendorNumber/ModelNumber/SerialNumber` 比对。
3. 同型号冲突时的兜底：`CoreDisplay_DisplayCreateInfoDictionary(id)["IODisplayLocation"]`（`dlsym`，取不到就放弃兜底）→ `IORegistryEntryCopyFromPath` → 沿 IOService 平面走到下一个 `DCPAVServiceProxy`。
4. 映射缓存，并在显示器配置变化回调与唤醒后重建；重建后对每台做一次 `VCPBrightness` 读取验证，避免陈旧 service 让 DDC 卡住。
5. `ddcTransport` 在绑不上时返回可识别错误（例如 `ErrNoHardwareControl`），由上层标记"硬件控制不可用"，**不得**回退到"随便拿一台"。

## 验收

- 单台外接：与 1.11.1 完全一致（回归）。
- 两台外接：`--probe` 对每台打印各自的 DDC 读数；对 A 设亮度不影响 B（真机手工验收见 [issue 09](09-hardware-verification.md)）。
- 两台同型号、序列号 0：不做贪心配对；配不上的那台返回 `ErrNoHardwareControl`。
- 私有符号缺失（未来 macOS 变化）时不 panic，整体降级为无硬件控制。

## 备注

`IOAVServiceCopyEDID` 传分配器当第二个参数会返回 `kIOReturnBadArgument`，只能传 `CFDataRef*`。`IODisplayCreateInfoDictionary` 在这条路径上返回 `unknown`，不要用它取位置。
