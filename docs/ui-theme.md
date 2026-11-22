# Larder 视觉主题与毛玻璃规范

> 适用版本：v1.1.1
> 主题方向：莫兰迪紫、低饱和、静态半透明分层；保留原有布局与信息密度，默认关闭高成本实时模糊和持续动画。

## 1. 设计目标

- **不改变产品结构**：侧边栏、顶栏、卡片、表单、弹窗的布局和交互保持原样。
- **保持莫兰迪紫**：以灰紫 `#8b7bc8` 为强调色，背景加入低饱和的薰衣草、灰粉和雾蓝。
- **玻璃必须有层次**：用半透明底色、亮边、内高光和投影形成层次，不依赖大面积实时模糊。
- **先保证可读性**：正文、表单和状态标签的对比度优先于透明度。
- **控制性能成本**：默认关闭 `backdrop-filter`、全屏极光动画、噪点层和 fixed 背景，空闲时 GPU 接近静止。

## 1.1 亮色 / 暗色切换

- 右上角 MCP 状态左侧有一个玻璃胶囊开关：**太阳 = 亮色模式，月亮 = 暗色模式**。
- 当前模式通过 `<html data-theme="light|dark">` 控制，不使用 `prefers-color-scheme` 作为唯一样式入口。
- 首次打开且没有本地选择时跟随系统；点击开关后写入 `localStorage["larder.theme"]`，刷新和重启后保持。
- 页面 `<head>` 在样式表加载前恢复主题，避免首屏闪烁；同时更新 `meta[name="theme-color"]`。
- 键盘与读屏可用：按钮有动态 `aria-label` / `aria-pressed`，支持 `:focus-visible`。
- 原生 Windows 标题栏通过 DWM 跟随 `data-theme`：亮色使用浅色标题栏与深色文字，暗色使用深色标题栏与浅色文字；切换时前端调用 `/api/system/window-theme` 即时刷新。

## 2. 颜色与材质 Token

主题变量集中在 `internal/web/dist/app.css` 的 `:root`（亮色）与 `:root[data-theme="dark"]`（暗色）中。

| Token | 浅色 | 暗色 | 用途 |
| --- | --- | --- | --- |
| `--bg` / `--bg-2` | `#f8f5fc` / `#f2edf9` | `#17141f` / `#201a2b` | 页面底色 |
| `--glass` | `rgba(249,246,253,.93)` | `rgba(28,24,38,.94)` | 普通半透明表面 |
| `--glass-strong` | `rgba(255,252,255,.97)` | `rgba(34,29,45,.97)` | 顶栏、按钮、弹窗 |
| `--glass-border` | `rgba(139,123,200,.16)` | `rgba(255,255,255,.10)` | 玻璃亮边 |
| `--glass-highlight` | `rgba(255,255,255,.90)` | `rgba(255,255,255,.14)` | 顶部内高光 |
| `--glass-shade` | `rgba(56,42,85,.06)` | `rgba(0,0,0,.30)` | 底部内阴影 |
| `--main-sheen` | 白色薄雾渐变 | 极轻白色高光 | 主面板提亮，避免比卡片更暗 |
| `--glass-blur` | `0px`（默认关闭） | `0px`（默认关闭） | 兼容变量；实际不启用 |
| `--glass-blur-strong` | `0px`（默认关闭） | `0px`（默认关闭） | 兼容变量；实际不启用 |
| `--accent` / `--accent-deep` | `#8b7bc8` / `#6d5aa8` | `#c3b5ee` / `#d8cdf6` | 强调色 |
| `--aura-1/2/3` | 薰衣草 / 灰粉 / 雾蓝 | 深紫 / 暗玫瑰 / 深蓝 | 背景极光 |

玻璃面的统一写法：

```css
background: var(--glass);
background-image: var(--glass-sheen);
border: 1px solid var(--glass-border);
box-shadow:
  var(--shadow-sm),
  inset 0 1px 0 var(--glass-highlight),
  inset 0 -1px 0 var(--glass-shade);
/* 默认 lightweight：不使用 backdrop-filter */
```

## 3. 背景与氛围

- `body` 使用三层低饱和径向渐变叠加线性渐变，保持莫兰迪紫的整体色调；亮色下主面板叠加白色薄雾，显得比卡片更轻而不是更深。
- `body::before` 的极光层和 `body::after` 的 SVG 噪点默认隐藏，避免全屏合成层。
- `body` 只保留三层静态径向渐变 + 线性渐变；`background-attachment: scroll`，滚动不重复合成整张背景。
- 背景层都不接收指针事件，不改变页面布局，也不影响滚动。

## 4. 组件规范

| 组件 | 处理 |
| --- | --- |
| 侧边栏 / 顶栏 | 高不透明度半透明底、内高光、底部细阴影；与内容区形成前后层级 |
| 知识库卡片 / 检索结果 | 普通玻璃 + 悬浮时上浮 2–4px，阴影加深，边框出现低饱和强调色 |
| 流水线磁贴 / 用量卡片 | 保留阶段色（解析蓝、嵌入棕、重排粉），玻璃底上叠阶段渐变 |
| 按钮 | 轻微顶部渐变、内高光和底部内阴影；主按钮使用莫兰迪紫渐变 |
| 表单 | 玻璃底、细边框、聚焦时 3px 低饱和紫色光环 |
| 弹窗 | 更不透明的表面与更大的浮层阴影；遮罩不使用模糊 |
| 导航选中项 | 玻璃药丸 + 紫色文字 + 柔和光晕，不使用高饱和实色 |

## 5. 动效与性能

- 默认不启用 `backdrop-filter`；侧栏、顶栏、卡片、弹窗、按钮统一使用静态半透明表面。
- 默认停止全屏极光漂移、Logo 浮动、导航圆点呼吸等持续动画。
- 卡片悬浮只改变 `transform`、`box-shadow`、`border-color` 和背景，不引入模糊。
- 实测 WebView2 GPU 3D 占用从约 56% 降到约 1%（空闲/静态页面采样）。
- `prefers-reduced-motion` / `prefers-reduced-transparency` 仍保留，作为进一步降级。

## 6. 可访问性

- 所有按钮、链接、输入框都有 `:focus-visible` 轮廓。
- 浅色主题提高了卡片不透明度、边框和阴影，避免白底白卡。
- 支持 `prefers-reduced-transparency: reduce`：关闭 `backdrop-filter`，使用接近不透明的表面和更低的极光强度。
- 支持 `prefers-reduced-motion: reduce`：停止极光、Logo 和圆点动画，取消悬浮位移。
- 提供 `@supports not (backdrop-filter: blur(1px))` 回退：不支持毛玻璃时使用不透明底色，文字仍可读。
- 正文颜色、状态标签和表单提示都保持在低饱和背景上的可读对比度。

## 7. 维护方式

- 调整全局色调：修改 `:root` 中的 `--bg`、`--accent`、`--aura-*`。
- 调整玻璃厚度：只改 `--glass-blur`、`--glass-blur-strong`、`--glass-sat`。
- 调整亮边/阴影：改 `--glass-highlight`、`--glass-shade`、`--shadow-sm`、`--shadow-lift`。
- 新增玻璃组件时，复用上述 token，不要再写独立的 `blur(30px)` 等魔法数字。
- 修改 `app.css` 后同步递增 `index.html` 里的 `app.css?v=N`，避免 WebView2 缓存旧样式。

## 8. 验收清单

- [x] 浅色 / 暗色两套主题均保持莫兰迪紫。
- [x] 右上角太阳/月亮开关可在亮色与暗色间即时切换，刷新后保持。
- [x] 侧栏、顶栏、卡片、按钮、表单、弹窗具有半透明底、亮边、内高光和浮层阴影。
- [x] 卡片悬浮有轻微上浮与阴影变化，不引发布局抖动。
- [x] 默认关闭背景极光动画、噪点层和 `backdrop-filter`，空闲 GPU 占用显著下降。
- [x] `prefers-reduced-transparency`、`prefers-reduced-motion`、`@supports` 回退均已写入 CSS。
- [x] 设置页、知识库页、文献库页、弹窗在 WebView2/Chromium 中正常渲染。
- [x] `app.css?v=56` / `app.js?v=74`，构建后资源内嵌版本一致。
