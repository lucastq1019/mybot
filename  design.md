## Agent OS 完整设计

### 核心模型

```
两层循环:

外层 while true        对话级，等待触发
    └── 内层 while true    任务级，工具链执行
            └── break: LLM返回最终答案 或 超出保护阈值
```

---

### 存储层结构

```
/brain
    ├── system_desc.md        角色定义，固定不变
    ├── hot_memory.md         高频记忆全量，每轮可能更新
    ├── memory_index.json     所有记忆的摘要索引，常驻context
    └── archive/
            └── YYYY-MM.md    冷记忆，按需读取
```

记忆条目分层：

```
IndexEntry:
    id, summary(20tokens内), category, priority
    disclosure_level, source, keywords, updated_at

MemoryPiece:
    id, content, category, source
    supersedes, related_ids, created_at
```

category分类：

```
preference    用户偏好，几乎每次都用 → 全量注入
procedure     操作流程，高频使用   → 全量注入
fact          客观事实，中频       → 只注入index
episodic      历史事件，低频       → 只注入index
```

---

### Context组装（每次调用重新组装）

```
固定层（每次必有）:
    system_desc
    hot_memory          preference + procedure 全量
    memory_index        所有条目的摘要索引

动态层（按需注入）:
    conversation_summary    有则注入
    archive片段             LLM通过工具按需召回

历史层（滑动窗口）:
    最近K轮对话原文         超出阈值的部分已被压缩进summary
```

硬限制：

```
memory_index        < 2000 tokens
hot_memory          < 3000 tokens
保留原文轮次        最近5轮
```

---

### 触发源

```
用户消息            主动交互
定时任务            cron，无用户输入
外部事件            webhook、文件变化、消息队列
工具返回结果        agent推动自身下一轮
```

---

### 主流程

```
触发
    │
    ▼
组装context
    │
    ▼
LLM调用
    │
    ├── 返回工具调用 → 执行工具 → 结果追加context → 再次LLM调用
    │                                               （内层循环）
    │
    └── 返回最终答案 → 输出给用户/系统
```

---

### 后处理（异步，不阻塞主流程）

```
轨道2 记忆写入（每轮触发）:
    判断本轮对话是否有值得记忆的内容
        └── 有 → write_memory
                    ├── 检查supersedes，避免重复
                    ├── 评估priority和disclosure_level
                    ├── 高priority → hot_memory
                    └── 低priority → archive
                    └── 更新memory_index

轨道3 历史压缩（条件触发）:
    历史轮次 > 阈值 或 历史tokens > 阈值
        └── 压缩旧轮次 → 追加进conversation_summary
```

并发保护：

```
主流程读memory    加读锁
后处理写memory    加写锁
```

---

### 工具集

```
读取类:
    read_memory(id)         召回Level1 hot片段
    read_archive(source)    召回Level2 完整原文

写入类:
    write_memory(content, category, priority)
    forget_memory(id)       降级或归档

维护类:
    consolidate_memory()    压缩hot_memory，合并重复，归档低priority
```

---

### 渐进式披露三层

```
Level0    memory_index常驻context    LLM扫描决定是否展开
Level1    read_memory工具召回        hot_memory具体片段
Level2    read_archive工具召回       archive完整原文
```

召回率决策：

```
召回率 > 60%    直接全量注入更划算
召回率 < 30%    渐进式召回更划算
preference/procedure类    始终全量，不走渐进式
```

---

### 保护机制

```
内层循环:
    工具调用次数上限        防死循环
    context token上限      兜底截断

后处理竞态:
    读写锁保护memory store

记忆膨胀:
    hot_memory超限 → 触发consolidate
    index超限      → summary进一步压缩
```

---

### 核心原则

```
1. LLM永远用最少信息做判断，只在需要时展开细节
2. 固定层只更新不增长，历史层滑动不堆叠
3. 用户输入只是触发源之一，agent能推动自身循环
4. 主流程同步返回，记忆和压缩异步处理
5. 渐进式披露只在空间真正成为瓶颈时才值得做
```