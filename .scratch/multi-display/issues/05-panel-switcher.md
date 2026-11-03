# 05 面板切换器与目标语义

Status: ready-for-agent
Blocked by: 03, 04

## 背景

面板现在直接读 `p.s.monitor`（[panel.go:107](../../../panel.go#L107) 起）、`p.s.modes`、`s.link` 等。多屏后统一改为读目标显示器，并在 header 加切换器。形态与规则见 [spec](../spec.md#面板)。

## 要做

1. header 的显示器名称变成下拉（`ui.Menu`）：每项「名称 · 接口」，勾选当前目标；只有一台外接时不显示箭头、退化为纯文字。
2. 四个分页（`overview`、`picture`、`modes`、`activity`）、`hero`、`caption`、`footer`、`updateBar` 全部改读目标；切换目标后保持当前分页。
3. footer：单台时保持 1.11.1 规则；多台时目标没有任何硬件通道要写「仅软件调光」；目标探测中显示 spinner。
4. HUD 在多台时带显示器名称（`hudView`），单台时保持原样。
5. 文案加到 `locales/en.json` 与 `zh-Hans.json`，运行 `go tool mygo generate`。
6. 截图快照（`MONITORBAR_SNAPSHOTS`）更新；新增一张两台显示器、切换器展开的快照。

## 验收

- 两台外接：打开面板即可切换；切到 B 后亮度、色温、输入源、分辨率都显示 B 的。
- 一台外接：与 1.11.1 的截图一致（切换器不出现）。
- 拔掉目标：面板不空、自动回退到另一台。
- `go test ./...` 通过，中英文案都齐全。

## 备注

面板位置由托盘图标决定（mygo 的 flyout 落点），不会跟着目标显示器走——header 已经显示目标名称，不要再为"面板在另一块屏"加额外提示。

## Comments

- 2026-11-03：无头截图（`ui.Tester.Image`）画不出原生弹出菜单，`docs/screenshots/switcher.png` 因此只显示 header 的切换器箭头，展开后的菜单项由 `TestSwitcher` 覆盖。
