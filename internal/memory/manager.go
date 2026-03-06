package memory

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"mybot/internal/config"
	"mybot/internal/llm"
)

const (
	// DefaultCacheMaxSize 默认缓存最大条目数
	DefaultCacheMaxSize = 100
	// DefaultCacheMaxAge 默认缓存最大存活时间（1小时）
	DefaultCacheMaxAge = time.Hour
	// DefaultFileSizeThreshold 默认文件大小阈值（超过此大小使用缓存）
	DefaultFileSizeThreshold = 100 * 1024 // 100KB
)

// MemoryManagerConfig MemoryManager 配置
type MemoryManagerConfig struct {
	CacheMaxSize        int           // 缓存最大条目数
	CacheMaxAge         time.Duration // 缓存最大存活时间
	FileSizeThreshold   int64         // 文件大小阈值（字节），超过此大小使用缓存
}

// DefaultMemoryManagerConfig 返回默认配置
func DefaultMemoryManagerConfig() *MemoryManagerConfig {
	return &MemoryManagerConfig{
		CacheMaxSize:      DefaultCacheMaxSize,
		CacheMaxAge:       DefaultCacheMaxAge,
		FileSizeThreshold: DefaultFileSizeThreshold,
	}
}

// MemoryManager 实现记忆管理接口
type MemoryManager struct {
	index            *MemoryIndex
	contentCache     *LRUCache
	config           *MemoryManagerConfig
}

// NewMemoryManager 创建新的 MemoryManager（使用默认配置）
func NewMemoryManager() (*MemoryManager, error) {
	return NewMemoryManagerWithConfig(DefaultMemoryManagerConfig())
}

// NewMemoryManagerWithConfig 使用指定配置创建 MemoryManager
func NewMemoryManagerWithConfig(config *MemoryManagerConfig) (*MemoryManager, error) {
	// 初始化 brain 目录
	if err := InitBrainDirectory(); err != nil {
		return nil, fmt.Errorf("failed to initialize brain directory: %w", err)
	}

	// 加载或构建索引
	idx, err := LoadOrBuildIndex()
	if err != nil {
		return nil, fmt.Errorf("failed to load or build index: %w", err)
	}

	return &MemoryManager{
		index:        idx,
		contentCache: NewLRUCache(config.CacheMaxSize, config.CacheMaxAge),
		config:       config,
	}, nil
}

// Recall 在内存索引中按关键词匹配，返回 topK 个 MemoryPiece
// categoryFilter: 可选，过滤 category（preference/fact/logic）
// domainFilter: 可选，过滤 domain（dev/learning/life）
func (m *MemoryManager) Recall(query string, topK int, categoryFilter string, domainFilter string) ([]MemoryPiece, error) {
	if topK <= 0 {
		topK = 5 // 默认返回 5 个
	}

	queryLower := strings.ToLower(query)
	queryWords := strings.Fields(queryLower)

	// 计算每个条目的相关性得分
	type scoredEntry struct {
		entry  IndexEntry
		score  float64
		pieces []MemoryPiece
	}

	var scored []scoredEntry

	for _, entry := range m.index.Entries {
		// Category 过滤
		if categoryFilter != "" && entry.Category != categoryFilter {
			continue
		}

		// Domain 过滤（从 source 路径判断）
		if domainFilter != "" && !m.matchesDomain(entry.Source, domainFilter) {
			continue
		}

		score := 0.0

		// 关键词匹配得分
		for _, keyword := range entry.Keywords {
			keywordLower := strings.ToLower(keyword)
			for _, qw := range queryWords {
				if strings.Contains(keywordLower, qw) || strings.Contains(qw, keywordLower) {
					score += 1.0
				}
			}
			if strings.Contains(keywordLower, queryLower) || strings.Contains(queryLower, keywordLower) {
				score += 2.0
			}
		}

		// 摘要匹配得分
		summaryLower := strings.ToLower(entry.Summary)
		for _, qw := range queryWords {
			if strings.Contains(summaryLower, qw) {
				score += 0.5
			}
		}

		// 优先级加权
		score = score * (1.0 + float64(entry.Priority)/10.0)

		if score > 0 {
			// 读取文件内容（使用缓存）
			content, err := m.getFileContent(entry.Source)
			if err != nil {
				continue // 跳过无法读取的文件
			}

			// 创建 MemoryPiece
			piece := MemoryPiece{
				Content:  content,
				Category: entry.Category,
				Source:   entry.Source,
				Priority: entry.Priority,
			}

			scored = append(scored, scoredEntry{
				entry:  entry,
				score:  score,
				pieces: []MemoryPiece{piece},
			})
		}
	}

	// 按得分排序
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})

	// 取 topK
	result := []MemoryPiece{}
	for i := 0; i < len(scored) && i < topK; i++ {
		result = append(result, scored[i].pieces...)
	}

	return result, nil
}

// RecallSimple 简化版 Recall（向后兼容，不过滤）
func (m *MemoryManager) RecallSimple(query string, topK int) ([]MemoryPiece, error) {
	return m.Recall(query, topK, "", "")
}

// RecallWithPreprocess 使用 LLM 预处理查询的 Recall
func (m *MemoryManager) RecallWithPreprocess(query string, topK int, useLLM bool) ([]MemoryPiece, error) {
	var categoryFilter, domainFilter string
	var processedQuery string

	if useLLM && (config.Config != nil && config.Config.LLM.Enabled || llm.IsAvailable()) {
		// 使用 LLM 预处理（按角色使用索引模型）
		llmClient, err := llm.NewClientForRole(llm.RoleIndex)
		if err == nil {
			result, err := llmClient.PreprocessQuery(query)
			if err == nil {
				// 使用预处理结果
				processedQuery = strings.Join(result.Keywords, " ")
				categoryFilter = result.Category
				domainFilter = result.Domain
			} else {
				// LLM 预处理失败，使用原始查询
				processedQuery = query
			}
		} else {
			processedQuery = query
		}
	} else {
		// 不使用 LLM，直接使用原始查询
		processedQuery = query
	}

	return m.Recall(processedQuery, topK, categoryFilter, domainFilter)
}

// matchesDomain 检查 source 是否匹配指定的 domain
func (m *MemoryManager) matchesDomain(source, domain string) bool {
	// 简单实现：根据文件路径和内容判断
	// dev: 包含 "开发" 或 "dev" 相关
	// learning: 包含 "学习" 或 "learning" 相关
	// life: 包含 "生活" 或 "life" 相关
	
	sourceLower := strings.ToLower(source)
	switch domain {
	case "dev":
		return strings.Contains(sourceLower, "dev") || 
		       strings.Contains(sourceLower, "开发") ||
		       strings.Contains(sourceLower, "项目")
	case "learning":
		return strings.Contains(sourceLower, "learning") || 
		       strings.Contains(sourceLower, "学习") ||
		       strings.Contains(sourceLower, "笔记")
	case "life":
		return strings.Contains(sourceLower, "life") || 
		       strings.Contains(sourceLower, "生活") ||
		       strings.Contains(sourceLower, "习惯")
	default:
		return true // 未知 domain，不过滤
	}
}

// getFileContent 获取文件内容（带 LRU 缓存）
func (m *MemoryManager) getFileContent(source string) (string, error) {
	// 检查缓存
	if content, ok := m.contentCache.Get(source); ok {
		return content, nil
	}

	// 读取文件
	fileInfo, err := os.Stat(source)
	if err != nil {
		return "", err
	}

	fileSize := fileInfo.Size()
	content, err := os.ReadFile(source)
	if err != nil {
		return "", err
	}

	contentStr := string(content)

	// 如果文件超过阈值，使用缓存；否则直接返回（小文件不缓存）
	if fileSize > m.config.FileSizeThreshold {
		m.contentCache.Set(source, contentStr, fileSize)
	}

	return contentStr, nil
}

// Consolidate 根据 topic 决定写入 hot.md 或 archive，并更新索引
func (m *MemoryManager) Consolidate(topic string, rawContent string) error {
	// 判断写入目标：身份/目标/偏好 -> hot；按日快照/总结 -> archive
	target := determineTarget(topic)

	var filePath string
	var err error

	if target == "hot" {
		// 写入 hot.md（根据 topic 写入对应区块）
		filePath = HotFile
		section := determineHotSection(topic)
		err = appendToHotFileWithSection(topic, rawContent, section)
	} else {
		// 写入 archive/YYYY-MM-DD.md
		filePath, err = EnsureArchiveFile(time.Now())
		if err == nil {
			err = appendToArchiveFile(filePath, rawContent)
		}
	}

	if err != nil {
		return fmt.Errorf("failed to write to %s: %w", filePath, err)
	}

	// 更新索引：重新构建该文件的索引条目
	content, err := os.ReadFile(filePath)
	if err == nil {
		entry := extractEntryForFile(filePath, string(content))
		m.updateIndexEntry(entry)
	}

	// 保存索引
	if err := SaveIndex(m.index); err != nil {
		return fmt.Errorf("failed to save index: %w", err)
	}

	// 清除缓存
	m.contentCache.Remove(filePath)

	return nil
}

// determineTarget 根据 topic 决定写入目标
func determineTarget(topic string) string {
	topicLower := strings.ToLower(topic)
	
	// hot 关键词：身份、目标、偏好、习惯、技术栈
	hotKeywords := []string{"身份", "目标", "偏好", "习惯", "技术栈", "我是谁", "自我认知"}
	for _, kw := range hotKeywords {
		if strings.Contains(topicLower, kw) {
			return "hot"
		}
	}

	// 默认为 archive
	return "archive"
}

// appendToHotFile 追加内容到 hot.md
func appendToHotFile(topic, content string) error {
	data, err := os.ReadFile(HotFile)
	if err != nil {
		return err
	}

	// 在文件末尾追加
	newContent := fmt.Sprintf("\n\n## %s\n\n%s\n", topic, content)
	data = append(data, []byte(newContent)...)

	return os.WriteFile(HotFile, data, 0644)
}

// appendToArchiveFile 追加内容到 archive 文件
func appendToArchiveFile(filePath, content string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}

	// 在文件末尾追加
	newContent := fmt.Sprintf("\n%s\n", content)
	data = append(data, []byte(newContent)...)

	return os.WriteFile(filePath, data, 0644)
}

// updateIndexEntry 更新索引条目
func (m *MemoryManager) updateIndexEntry(entry IndexEntry) {
	// 查找现有条目
	for i, e := range m.index.Entries {
		if e.Source == entry.Source {
			// 更新现有条目
			m.index.Entries[i] = entry
			return
		}
	}

	// 添加新条目
	m.index.Entries = append(m.index.Entries, entry)
}

// extractEntryForFile 从文件内容中提取索引条目（简化版，用于增量更新）
func extractEntryForFile(source, content string) IndexEntry {
	entry := IndexEntry{
		Source:   source,
		Keywords: []string{},
		Summary:  "",
		Category: determineCategory(source, content),
		Priority: determinePriority(source),
	}

	// 提取标题作为关键词
	titleRegex := regexp.MustCompile(`^#+\s+(.+)$`)
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		if matches := titleRegex.FindStringSubmatch(line); len(matches) > 1 {
			title := strings.TrimSpace(matches[1])
			title = regexp.MustCompile(`\[([^\]]+)\]\([^\)]+\)`).ReplaceAllString(title, "$1")
			if title != "" {
				entry.Keywords = append(entry.Keywords, title)
			}
		}
	}

	// 提取摘要
	summary := extractSummary(content)
	if len(summary) > 200 {
		summary = summary[:200] + "..."
	}
	entry.Summary = summary

	return entry
}

// extractSummary 提取摘要（使用 build.go 中的函数）
func extractSummary(content string) string {
	return extractSummaryFromContent(content)
}

// SummarizeAndRotate 压缩调度：选择 archive 文件，调用 LLM 生成摘要，写回 MD，更新索引
func (m *MemoryManager) SummarizeAndRotate() error {
	// 检查 LLM 是否可用
	if !llm.IsAvailable() {
		return fmt.Errorf("LLM client not available: OPENAI_API_KEY not set")
	}

	// 1. 选择需要压缩的 archive 文件（选择较旧的或较大的文件）
	filesToSummarize, err := m.selectFilesToSummarize()
	if err != nil {
		return fmt.Errorf("failed to select files: %w", err)
	}

	if len(filesToSummarize) == 0 {
		log.Println("No files need to be summarized")
		return nil
	}

	log.Printf("Selected %d archive files to summarize", len(filesToSummarize))

	// 2. 读取文件内容并合并
	combinedContent, filePaths, err := m.readAndCombineFiles(filesToSummarize)
	if err != nil {
		return fmt.Errorf("failed to read files: %w", err)
	}

	if combinedContent == "" {
		log.Println("No content to summarize")
		return nil
	}

	// 3. 调用 LLM 生成摘要
	// 尝试从配置读取 LLM 设置
	llmClient, err := m.createLLMClient()
	if err != nil {
		return fmt.Errorf("failed to create LLM client: %w", err)
	}

	instructions := fmt.Sprintf(
		"你是一个专业的记忆摘要助手。请为以下多日的 archive 内容生成一个简洁、结构化的摘要。" +
		"摘要应该保留关键信息、重要事件和决策，使用 Markdown 格式。",
	)

	summary, err := llmClient.Summarize(combinedContent, instructions)
	if err != nil {
		return fmt.Errorf("failed to generate summary: %w", err)
	}

	// 4. 写回 MD 文件（创建 summary-YYYY-MM.md）
	now := time.Now()
	summaryFileName := fmt.Sprintf("summary-%s.md", now.Format("2006-01"))
	summaryFilePath := filepath.Join(ArchiveDir, summaryFileName)

	// 如果文件已存在，追加内容；否则创建新文件
	summaryContent := fmt.Sprintf("# %s 摘要\n\n生成时间: %s\n\n## 原始文件\n\n", 
		now.Format("2006-01"), now.Format("2006-01-02 15:04:05"))
	for _, path := range filePaths {
		summaryContent += fmt.Sprintf("- %s\n", filepath.Base(path))
	}
	summaryContent += fmt.Sprintf("\n## 摘要内容\n\n%s\n", summary)

	// 追加到摘要文件（如果已存在）
	if _, err := os.Stat(summaryFilePath); err == nil {
		existingContent, _ := os.ReadFile(summaryFilePath)
		summaryContent = string(existingContent) + "\n\n---\n\n" + summaryContent
	}

	if err := os.WriteFile(summaryFilePath, []byte(summaryContent), 0644); err != nil {
		return fmt.Errorf("failed to write summary file: %w", err)
	}

	log.Printf("Summary written to: %s", summaryFilePath)

	// 5. 移动原始文件到 backup 目录（开发测试阶段使用 backup，生产环境使用 git 或备份命令）
	backupDir := filepath.Join(ArchiveDir, "backup")
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		log.Printf("Warning: failed to create backup directory: %v", err)
	}

	for _, filePath := range filePaths {
		backupPath := filepath.Join(backupDir, filepath.Base(filePath))
		if err := os.Rename(filePath, backupPath); err != nil {
			log.Printf("Warning: failed to move file to backup %s: %v", filePath, err)
			// 如果移动失败，尝试删除（向后兼容）
			if err := os.Remove(filePath); err != nil {
				log.Printf("Warning: failed to remove file %s: %v", filePath, err)
			}
		} else {
			log.Printf("Moved original file to backup: %s -> %s", filePath, backupPath)
		}
		// 从索引中移除该文件的条目（无论移动还是删除）
		m.removeIndexEntry(filePath)
	}

	// 6. 更新索引：添加摘要文件的条目
	entry := extractEntryForFile(summaryFilePath, summaryContent)
	m.updateIndexEntry(entry)

	// 保存索引
	if err := SaveIndex(m.index); err != nil {
		return fmt.Errorf("failed to save index: %w", err)
	}

	// 清除缓存
	for _, path := range filePaths {
		m.contentCache.Remove(path)
	}
	m.contentCache.Remove(summaryFilePath)

	log.Println("SummarizeAndRotate completed successfully")
	return nil
}

// selectFilesToSummarize 选择需要压缩的 archive 文件
// 策略：选择最旧的或较大的文件，最多选择 10 个文件或总大小不超过 5MB
func (m *MemoryManager) selectFilesToSummarize() ([]string, error) {
	entries, err := os.ReadDir(ArchiveDir)
	if err != nil {
		return nil, err
	}

	type fileInfo struct {
		path     string
		size     int64
		modTime  time.Time
	}

	var files []fileInfo
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		// 只处理 .md 文件
		if filepath.Ext(entry.Name()) != ".md" {
			continue
		}

		// 跳过 summary 文件
		if strings.HasPrefix(entry.Name(), "summary-") {
			continue
		}

		filePath := filepath.Join(ArchiveDir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			continue
		}

		files = append(files, fileInfo{
			path:    filePath,
			size:    info.Size(),
			modTime: info.ModTime(),
		})
	}

	// 按修改时间排序（最旧的在前）
	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime.Before(files[j].modTime)
	})

	// 选择文件：最多 10 个文件或总大小不超过 5MB
	const maxFiles = 10
	const maxTotalSize = 5 * 1024 * 1024 // 5MB

	var selected []string
	var totalSize int64

	for _, file := range files {
		if len(selected) >= maxFiles {
			break
		}
		if totalSize+file.size > maxTotalSize && len(selected) > 0 {
			break
		}

		selected = append(selected, file.path)
		totalSize += file.size
	}

	return selected, nil
}

// readAndCombineFiles 读取并合并多个文件的内容
func (m *MemoryManager) readAndCombineFiles(filePaths []string) (string, []string, error) {
	var combined strings.Builder
	var validPaths []string

	for _, path := range filePaths {
		content, err := m.getFileContent(path)
		if err != nil {
			log.Printf("Warning: failed to read file %s: %v", path, err)
			continue
		}

		if strings.TrimSpace(content) == "" {
			continue
		}

		combined.WriteString(fmt.Sprintf("\n\n---\n\n# %s\n\n%s", filepath.Base(path), content))
		validPaths = append(validPaths, path)
	}

	return combined.String(), validPaths, nil
}

// removeIndexEntry 从索引中移除条目
func (m *MemoryManager) removeIndexEntry(source string) {
	for i, entry := range m.index.Entries {
		if entry.Source == source {
			// 删除条目
			m.index.Entries = append(m.index.Entries[:i], m.index.Entries[i+1:]...)
			return
		}
	}
}

// GetIndex 获取索引（供外部使用）
func (m *MemoryManager) GetIndex() *MemoryIndex {
	return m.index
}

// createLLMClient 创建 LLM 客户端（从配置读取）
func (m *MemoryManager) createLLMClient() (*llm.Client, error) {
	// 使用角色化工厂函数创建，用于记忆压缩/长文摘要等场景
	return llm.NewClientForRole(llm.RoleCompress)
}

// CheckSummarizeTrigger 检查是否应该触发摘要
func (m *MemoryManager) CheckSummarizeTrigger() (bool, string) {
	trigger := NewSummarizeTrigger(DefaultMaxArchiveFiles, DefaultMaxArchiveSize)
	return trigger.ShouldSummarize()
}
