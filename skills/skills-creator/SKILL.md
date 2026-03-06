---
name: skills-creator
description: 定义并创建本项目技能的标准格式与流程。当 skills-index 无匹配技能、需新增重复任务或标准化流程或封装专业知识时使用；创建后须经 skill-validator 校验并写入 skills-index。
version: 1.0.0
author: CLAW Team
tags: [agent, skill-creation, specification]
dependencies: []
---

# skills-creator

## 触发

- 需新增技能（重复任务、标准化流程、封装专业知识、跨会话复用）
- 读 `skills/skills-index.json` 后无匹配项

## 规范

### Frontmatter

与 skill-validator 一致，新建技能须包含：

```yaml
---
name: skill-name
description: 简要描述与调用时机
version: 1.0.0
author: CLAW Team
tags: [tag1, tag2]
dependencies: []
---
```

| 字段 | 要求 |
|------|------|
| `name` | 小写、连字符、≤64 字符，与目录名一致 |
| `description` | 非空，建议 ≤200 字；含「做什么」与「何时用」 |
| `dependencies` | 仅直接依赖技能名，禁止循环 |

### 路径与目录

| 类型 | 路径 |
|------|------|
| 技能根目录 | `skills/` |
| 单技能 | `skills/<name>/SKILL.md` |
| 可选 | `skills/<name>/resources/` |

### SKILL.md 正文结构

- **触发条件**：何时调用本技能
- **执行步骤**：按序、可校验
- **输入/输出或约定**：明确接口
- **可选**：最佳实践、注意事项（简短）

命名：仅小写与连字符，如 `code-generator`、`document-parser`。

## 创建流程

按序执行并勾选：

```
- [ ] 1. 定 name、description、tags、dependencies
- [ ] 2. 建目录 skills/<name>/，写 SKILL.md（frontmatter + 上述正文结构）
- [ ] 3. 调用 skill-validator 校验；若有问题先报告、获确认后修复并复验
- [ ] 4. 将新技能加入 skills/skills-index.json：skills 数组 + tags_index 中各 tag 对应 name
```

**步骤 4 约定**：在 `skills` 中追加一项（含 name、path、description、version、author、tags、dependencies）；在 `tags_index` 中为每个 tag 在对应数组中追加本技能 `name`（若 key 不存在则新建数组）。
